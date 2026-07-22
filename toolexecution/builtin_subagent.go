package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
)

const (
	StartSubagentToolName       = "start_subagent"
	ListSubagentsToolName       = "list_subagents"
	SubagentStatusToolName      = "subagent_status"
	SendSubagentMessageToolName = "send_subagent_message"
	CloseSubagentToolName       = "close_subagent"
)

var subagentToolNames = map[string]struct{}{
	StartSubagentToolName: {}, ListSubagentsToolName: {}, SubagentStatusToolName: {},
	SendSubagentMessageToolName: {}, CloseSubagentToolName: {},
}

// IsSubagentToolName reports whether name is reserved by the subagent built-ins.
func IsSubagentToolName(name string) bool {
	_, ok := subagentToolNames[name]
	return ok
}

// SubagentController supplies lifecycle operations to the built-in handlers.
// agentcli implements this interface without exposing its runtime manager.
type SubagentController interface {
	StartOrReuse(context.Context, string, string, string, string, string, bool) (SubagentStartResult, error)
	List(context.Context, string, bool) ([]storage.Subagent, error)
	SendFromParentTurn(context.Context, string, string, string, string) (SubagentSendResult, error)
	CloseSubagent(context.Context, string, string) (storage.Subagent, error)
}

// SubagentStartAction describes how the conversational start request was
// routed. Direct application APIs may still create child instances explicitly.
type SubagentStartAction string

const (
	SubagentStartCreated           SubagentStartAction = "created"
	SubagentStartReused            SubagentStartAction = "reused"
	SubagentStartSelectionRequired SubagentStartAction = "selection_required"
)

// SubagentStartResult keeps routing decisions out of provider-specific tool
// handlers. Selection candidates are lightweight child records owned by the
// same parent session.
type SubagentStartResult struct {
	Action         SubagentStartAction
	DispatchAction SubagentSendAction
	Subagent       storage.Subagent
	Candidates     []storage.Subagent
}

// SubagentSendAction describes how one parent-turn message was handled.
type SubagentSendAction string

const (
	SubagentSendStarted     SubagentSendAction = "started"
	SubagentSendQueued      SubagentSendAction = "queued"
	SubagentSendDuplicate   SubagentSendAction = "duplicate"
	SubagentSendAlreadySent SubagentSendAction = "already_sent"
)

// SubagentSendResult exposes the enforced parent-turn idempotency decision.
type SubagentSendResult struct {
	Action         SubagentSendAction
	Subagent       storage.Subagent
	IdempotencyKey string
	Deduplicated   bool
	Accepted       bool
}

// SubagentToolBridge allows tools to be registered before agentcli can create
// and bind its controller. Handlers resolve the controller at invocation time.
type SubagentToolBridge struct {
	mu         sync.RWMutex
	controller SubagentController
}

func NewSubagentToolBridge() *SubagentToolBridge { return &SubagentToolBridge{} }

func (bridge *SubagentToolBridge) Bind(controller SubagentController) {
	bridge.mu.Lock()
	bridge.controller = controller
	bridge.mu.Unlock()
}

func (bridge *SubagentToolBridge) get() (SubagentController, error) {
	bridge.mu.RLock()
	controller := bridge.controller
	bridge.mu.RUnlock()
	if controller == nil {
		return nil, errors.New("subagent manager is unavailable")
	}
	return controller, nil
}

// Tools returns the static parent-only subagent built-in catalog. Child
// agents never register this bridge.
func (bridge *SubagentToolBridge) Tools() []Tool {
	return []Tool{
		bridge.tool(StartSubagentToolName, "Route substantial delegated work to a project-defined subagent. By default, no open child creates one, exactly one open child is reused, and multiple open children return a selection_required result so you must ask the user which friendly name to continue with. Set new_instance=true only when the user explicitly asks for a new, another, separate, or parallel child. For a follow-up naming an existing child, use send_subagent_message with the ID mapped from active_subagents. Do not use this tool for simple answers or conversational work the parent can handle directly. Child turns are always asynchronous; never poll after routing work.", `{"type":"object","properties":{"name":{"type":"string","description":"Exact subagent definition name from available_subagents; used when a new instance is created"},"message":{"type":"string","description":"A focused delegated task or follow-up message"},"label":{"type":"string","description":"Optional task label; the runtime assigns every child a separate friendly random display name"},"new_instance":{"type":"boolean","description":"Create a distinct child even when open children exist. Use only for explicit new, another, separate, or parallel intent; defaults to false."}},"required":["name","message"],"additionalProperties":false}`, bridge.start),
		bridge.tool(ListSubagentsToolName, "List lightweight lifecycle summaries only when the user explicitly asks to discover or enumerate subagents. This does not return findings. Never call it to wait, poll, monitor completion, or check whether a callback has arrived; callbacks arrive automatically.", `{"type":"object","properties":{"include_closed":{"type":"boolean"}},"additionalProperties":false}`, bridge.list),
		bridge.tool(SubagentStatusToolName, "Get one subagent's lightweight lifecycle status only when the user explicitly asks for current status or an immediate operation truly requires it. This does not return findings. Call at most once for that question, answer from the snapshot, and never poll, retry, or use it while waiting for callbacks.", `{"type":"object","properties":{"subagent_id":{"type":"string"}},"required":["subagent_id"],"additionalProperties":false}`, bridge.status),
		bridge.tool(SendSubagentMessageToolName, "Send one focused follow-up to an identified existing child. Resolve the user's friendly display name to its id through active_subagents. The runtime accepts at most one message from the same parent turn to the same child: exact repeats are deduplicated and changed repeats return already_sent without queueing. If accepted while the child is busy the message is queued; otherwise a new asynchronous child turn starts. Do not poll or call this tool again to wait: finish the parent turn or do other useful work, and use the next callback as the result.", `{"type":"object","properties":{"subagent_id":{"type":"string"},"message":{"type":"string","description":"One focused missing detail, clarification, recovery request, or next task"}},"required":["subagent_id","message"],"additionalProperties":false}`, bridge.send),
		bridge.tool(CloseSubagentToolName, "Close an owned subagent while retaining its transcript history. Close bounded one-shot work after its result is consumed and delivered unless there is a concrete planned follow-up, queued or unresolved work requiring the same context, or explicit ongoing collaboration. The mere possibility of a later user question is not a reason to keep it open.", `{"type":"object","properties":{"subagent_id":{"type":"string"}},"required":["subagent_id"],"additionalProperties":false}`, bridge.close),
	}
}

func (bridge *SubagentToolBridge) tool(name, description, schema string, handler Handler) Tool {
	return Tool{Definition: agentruntime.ToolDefinition{Name: name, Description: description, InputSchema: json.RawMessage(schema)}, Handler: handler}
}

func subagentInvocation(ctx context.Context, name string) (Invocation, error) {
	invocation, ok := InvocationFromContext(ctx)
	if !ok || invocation.ToolName != name {
		return Invocation{}, fmt.Errorf("%s requires tool invocation context", name)
	}
	return invocation, nil
}

func decodeSubagentTool(arguments json.RawMessage, output any) error {
	decoder := json.NewDecoder(strings.NewReader(string(arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode subagent tool arguments: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode subagent tool arguments: multiple JSON values")
		}
		return fmt.Errorf("decode subagent tool arguments: %w", err)
	}
	return nil
}

// SubagentToolSummary is the stable JSON-facing child state projection.
type SubagentToolSummary struct {
	ID               string                      `json:"id"`
	DisplayName      string                      `json:"display_name"`
	Label            string                      `json:"label,omitempty"`
	SessionID        string                      `json:"session_id"`
	DefinitionName   string                      `json:"definition_name"`
	Provider         string                      `json:"provider"`
	Model            string                      `json:"model"`
	Status           storage.SubagentStatus      `json:"status"`
	CurrentTurnID    string                      `json:"current_turn_id,omitempty"`
	LastTurnID       string                      `json:"last_turn_id,omitempty"`
	LastTurnError    string                      `json:"last_turn_error,omitempty"`
	LastTurnOutcome  storage.SubagentTurnOutcome `json:"last_turn_outcome,omitempty"`
	LastTurnSummary  string                      `json:"last_turn_summary,omitempty"`
	LastTurnNextStep string                      `json:"last_turn_next_step,omitempty"`
	Version          uint64                      `json:"version"`
	QueuedMessages   int                         `json:"queued_messages"`
}

// SubagentStatusResult is a small, transcript-free status projection.
type SubagentStatusResult struct {
	Subagent        SubagentToolSummary `json:"subagent"`
	ActivitySummary string              `json:"activity_summary"`
	ResultReady     bool                `json:"result_ready"`
}

func summarizeSubagent(record storage.Subagent) SubagentToolSummary {
	return SubagentToolSummary{ID: record.ID, DisplayName: record.DisplayName, Label: record.Label, SessionID: record.SessionID, DefinitionName: record.DefinitionName, Provider: record.Provider, Model: record.Model, Status: record.Status, CurrentTurnID: record.CurrentTurnID, LastTurnID: record.LastTurnID, LastTurnError: record.LastTurnError, LastTurnOutcome: record.LastTurnOutcome, LastTurnSummary: record.LastTurnSummary, LastTurnNextStep: record.LastTurnNextStep, Version: record.Version, QueuedMessages: len(record.Pending)}
}

func summarizeSubagents(records []storage.Subagent) []SubagentToolSummary {
	result := make([]SubagentToolSummary, len(records))
	for index, record := range records {
		result[index] = summarizeSubagent(record)
	}
	return result
}

func summarizeSubagentActivity(record storage.Subagent) string {
	task := strings.TrimSpace(record.Label)
	if task == "" {
		task = record.DefinitionName
	}
	var activity string
	switch {
	case record.Status == storage.SubagentStatusRunning:
		activity = "Working on: " + task
	case record.LastTurnError != "":
		activity = "Last turn failed: " + record.LastTurnError
	case record.Status == storage.SubagentStatusIdle && record.LastTurnOutcome == storage.SubagentTurnIncomplete:
		activity = "Incomplete: " + task + "; next: " + record.LastTurnNextStep
	case record.Status == storage.SubagentStatusIdle && record.LastTurnID != "":
		activity = "Completed: " + task
	case record.Status == storage.SubagentStatusClosed:
		activity = "Closed: " + task
	default:
		activity = "Idle: " + task
	}
	if queued := len(record.Pending); queued != 0 {
		activity += fmt.Sprintf("; %d follow-up message(s) queued", queued)
	}
	return activity
}

func (bridge *SubagentToolBridge) start(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Name        string `json:"name"`
		Message     string `json:"message"`
		Label       string `json:"label"`
		NewInstance bool   `json:"new_instance"`
	}
	if err := decodeSubagentTool(arguments, &input); err != nil {
		return nil, err
	}
	invocation, err := subagentInvocation(ctx, StartSubagentToolName)
	if err != nil {
		return nil, err
	}
	controller, err := bridge.get()
	if err != nil {
		return nil, err
	}
	result, err := controller.StartOrReuse(ctx, invocation.SessionID, invocation.TurnID, input.Name, input.Message, input.Label, input.NewInstance)
	if err != nil {
		return nil, err
	}
	if result.Action == SubagentStartSelectionRequired {
		return json.Marshal(struct {
			Action       SubagentStartAction   `json:"action"`
			Candidates   []SubagentToolSummary `json:"candidates"`
			Asynchronous bool                  `json:"asynchronous"`
			NextAction   string                `json:"next_action"`
		}{result.Action, summarizeSubagents(result.Candidates), false, "Ask the user which friendly display_name to continue with. Do not choose for them and do not create another child. After they answer, call send_subagent_message with that candidate's id."})
	}
	record := result.Subagent
	nextAction := "The child turn is running asynchronously. Address it by display_name when speaking to the user, use its id for tools, and do not poll. Continue useful independent work or finish the response; completion is delivered separately."
	if result.DispatchAction == SubagentSendDuplicate || result.DispatchAction == SubagentSendAlreadySent {
		nextAction = "This parent turn already routed a message to the child. Nothing new was queued. Do not send again or poll; finish the response and wait for its callback."
	}
	return json.Marshal(struct {
		SubagentID     string                 `json:"subagent_id"`
		DisplayName    string                 `json:"display_name"`
		SessionID      string                 `json:"session_id"`
		TurnID         string                 `json:"turn_id"`
		Status         storage.SubagentStatus `json:"status"`
		Action         SubagentStartAction    `json:"action"`
		DispatchAction SubagentSendAction     `json:"dispatch_action,omitempty"`
		Reused         bool                   `json:"reused"`
		Asynchronous   bool                   `json:"asynchronous"`
		NextAction     string                 `json:"next_action"`
	}{record.ID, record.DisplayName, record.SessionID, record.CurrentTurnID, record.Status, result.Action, result.DispatchAction, result.Action == SubagentStartReused, true, nextAction})
}

func (bridge *SubagentToolBridge) list(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input struct {
		IncludeClosed bool `json:"include_closed"`
	}
	if err := decodeSubagentTool(arguments, &input); err != nil {
		return nil, err
	}
	invocation, err := subagentInvocation(ctx, ListSubagentsToolName)
	if err != nil {
		return nil, err
	}
	controller, err := bridge.get()
	if err != nil {
		return nil, err
	}
	records, err := controller.List(ctx, invocation.SessionID, input.IncludeClosed)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Subagents []SubagentToolSummary `json:"subagents"`
	}{summarizeSubagents(records)})
}

func (bridge *SubagentToolBridge) status(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input struct {
		ID string `json:"subagent_id"`
	}
	if err := decodeSubagentTool(arguments, &input); err != nil {
		return nil, err
	}
	invocation, err := subagentInvocation(ctx, SubagentStatusToolName)
	if err != nil {
		return nil, err
	}
	controller, err := bridge.get()
	if err != nil {
		return nil, err
	}
	records, err := controller.List(ctx, invocation.SessionID, true)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.ID != input.ID {
			continue
		}
		return json.Marshal(SubagentStatusResult{
			Subagent:        summarizeSubagent(record),
			ActivitySummary: summarizeSubagentActivity(record),
			ResultReady:     record.Status == storage.SubagentStatusIdle && record.LastTurnOutcome == storage.SubagentTurnCompleted,
		})
	}
	return nil, storage.ErrSubagentNotFound
}

func (bridge *SubagentToolBridge) send(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input struct {
		ID      string `json:"subagent_id"`
		Message string `json:"message"`
	}
	if err := decodeSubagentTool(arguments, &input); err != nil {
		return nil, err
	}
	invocation, err := subagentInvocation(ctx, SendSubagentMessageToolName)
	if err != nil {
		return nil, err
	}
	controller, err := bridge.get()
	if err != nil {
		return nil, err
	}
	result, err := controller.SendFromParentTurn(ctx, invocation.SessionID, invocation.TurnID, input.ID, input.Message)
	if err != nil {
		return nil, err
	}
	instruction := "Message accepted. The child will callback asynchronously; do not send again or poll in this parent turn."
	if !result.Accepted {
		instruction = "A message was already sent to this child from this parent turn. Nothing new was queued. End this parent turn and wait for the callback."
	}
	return json.Marshal(struct {
		Action       SubagentSendAction  `json:"action"`
		Accepted     bool                `json:"accepted"`
		Deduplicated bool                `json:"deduplicated"`
		Subagent     SubagentToolSummary `json:"subagent"`
		Instruction  string              `json:"instruction"`
	}{result.Action, result.Accepted, result.Deduplicated, summarizeSubagent(result.Subagent), instruction})
}

func (bridge *SubagentToolBridge) close(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input struct {
		ID string `json:"subagent_id"`
	}
	if err := decodeSubagentTool(arguments, &input); err != nil {
		return nil, err
	}
	invocation, err := subagentInvocation(ctx, CloseSubagentToolName)
	if err != nil {
		return nil, err
	}
	controller, err := bridge.get()
	if err != nil {
		return nil, err
	}
	record, err := controller.CloseSubagent(ctx, invocation.SessionID, input.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(summarizeSubagent(record))
}
