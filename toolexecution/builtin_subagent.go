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
	StartSubagentToolName           = "start_subagent"
	ListSubagentsToolName           = "list_subagents"
	SubagentStatusToolName          = "subagent_status"
	SendSubagentMessageToolName     = "send_subagent_message"
	subagentWaitInstruction         = "Continue only work already planned before dispatch that is outside the delegated task and independent of its callback. If none remains, end the turn immediately without assistant content or another tool call."
	subagentSendAcceptedInstruction = "Accepted. The result will arrive automatically later. " + subagentWaitInstruction
)

var subagentToolNames = map[string]struct{}{
	StartSubagentToolName:       {},
	SendSubagentMessageToolName: {},
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
	StatusFromParentTurn(context.Context, string, string, string) (SubagentStatusSnapshot, error)
	SendFromParentTurn(context.Context, string, string, string, string) (SubagentSendResult, error)
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
	Deduplicated   bool
	Accepted       bool
}

// SubagentSendAction describes how one parent-turn message was handled.
type SubagentSendAction string

const (
	SubagentSendStarted         SubagentSendAction = "started"
	SubagentSendQueued          SubagentSendAction = "queued"
	SubagentSendDuplicate       SubagentSendAction = "duplicate"
	SubagentSendAlreadySent     SubagentSendAction = "already_sent"
	SubagentSendCallbackPending SubagentSendAction = "callback_pending"
)

// SubagentSendResult exposes the enforced parent-turn idempotency decision.
type SubagentSendResult struct {
	Action         SubagentSendAction
	Subagent       storage.Subagent
	IdempotencyKey string
	Deduplicated   bool
	Accepted       bool
}

// SubagentCloseResult describes the destructive lifecycle state removed by an
// application-owned close.
type SubagentCloseResult struct {
	Subagent        storage.Subagent
	PreviousStatus  storage.SubagentStatus
	PreviousOutcome storage.SubagentTurnOutcome
	DroppedMessages int
	Interrupted     bool
}

// SubagentStatusSnapshot is one cached lifecycle observation for a child in a
// parent turn. Repeated reads return the original record rather than polling.
type SubagentStatusSnapshot struct {
	Subagent storage.Subagent
	Repeated bool
}

// SubagentToolBridge allows tools to be registered before agentcli can create
// and bind its controller. Handlers resolve the controller at invocation time.
type SubagentToolBridge struct {
	mu         sync.RWMutex
	controller SubagentController
}

func NewSubagentToolBridge() *SubagentToolBridge {
	return &SubagentToolBridge{}
}

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
		bridge.tool(StartSubagentToolName, "", `{"type":"object","properties":{"name":{"type":"string","minLength":1,"description":"Exact configured type name from available_subagents, selected because its description directly matches the focused delegated task or an applicable instruction explicitly requires it. This is not a child ID or display_name."},"message":{"type":"string","minLength":1,"description":"Self-contained focused delegated task including relevant context, constraints, and the result expected from the child. Never use this field to ask for status, send a reminder, or chase running work."},"label":{"type":"string","minLength":1,"maxLength":120,"description":"Optional short UI label for this delegated task. Do not put instructions here."},"new_instance":{"type":"boolean","default":false,"description":"True is required for a genuinely new assignment that must run in a separate child, even when a child of the same definition is already open. False creates a child when none is open but explicitly permits reuse of the sole open child; choose it only when that possible reuse is acceptable. Never use new_instance=true to bypass pending work or as a retry mechanism."},"continue_after_dispatch":{"type":"boolean","description":"Required turn choice made before dispatch. Set false when no already-planned parent work outside the delegated task must run immediately afterward; a successful pending-callback tool batch then ends the turn automatically. Set true only when such independent work already exists and must continue immediately. For multiple start_subagent calls in one tool batch, use the same value on every call: all false to wait after the batch, or all true to continue independent parent work. Never mix values; any false call that returns a pending callback ends the successful batch."}},"required":["name","message","continue_after_dispatch"],"additionalProperties":false}`, bridge.start),
		bridge.tool(SendSubagentMessageToolName, "", `{"type":"object","properties":{"subagent_id":{"type":"string","minLength":1,"description":"ID of an idle child from a callback already received and consumed. Never use an active or running child, a definition name, or display_name."},"message":{"type":"string","minLength":1,"description":"One self-contained focused follow-up, recovery instruction, or distinct next task for that idle child. Do not send status checks, reminders, waiting requests, or repeat already-delegated work."}},"required":["subagent_id","message"],"additionalProperties":false}`, bridge.send),
	}
}

func (bridge *SubagentToolBridge) tool(name, description, schema string, handler Handler) Tool {
	switch name {
	case StartSubagentToolName:
		description = "Start a configured subagent or route a focused assignment to an existing child after a valid delegation trigger. This is not a status, reminder, or follow-up tool for running work: when a child has a pending callback, wait for its automatic callback before interacting with that child again. Waiting on that child still allows already-planned parent work outside the delegated task that is independent of the callback. Valid triggers are: (1) a definition description directly matches the focused task and delegation materially helps through specialized independent work, substantial context isolation, or useful parallelism; or (2) an applicable instruction or the user explicitly requires delegation or that subagent type. Topic overlap, discovery-only questions, and simple self-contained work do not trigger this tool by themselves; an applicable explicit requirement remains a valid trigger. Select the exact configured type from available_subagents; its description is selection metadata, not proof that work started. For a genuinely new assignment that must have a separate child, set new_instance=true even if another child of the same definition is open. new_instance=false creates a child when none is open but reuses the sole open child when unambiguous; choose it only when that possible reuse is acceptable. To continue a specific idle child after consuming its callback, use send_subagent_message with its id. Before calling, explicitly choose continue_after_dispatch. Set it to false when no already-planned parent work outside the delegated task must run immediately afterward; a successful result with a pending callback then ends the current turn after the whole tool batch succeeds, without another provider step or assistant content. Set it to true only when specific independent parent work was already planned and must continue immediately; never invent work to justify true. For multiple start_subagent calls in one tool batch, use the same value on all calls: all false to wait after the batch, or all true to continue independent parent work. Never mix values because any false call that returns a pending callback ends the successful batch. Prefer one child at a time; ordinary lookup or research should assess one callback before starting another. Multiple starts in one response are only for genuinely independent comparison or parallel work. accepted=true means dispatched, never completed; accepted=false means no new dispatch and must not be retried. selection_required always continues because it has no pending callback. The result arrives automatically at a provider boundary of the active parent or in a callback continuation turn. After any result with a pending callback, do not poll, inspect status, retry, or redo the delegated work."
	case SendSubagentMessageToolName:
		description = "Send one focused message to an existing idle child only, after a valid continuation trigger and after its latest callback has been consumed. Valid triggers are: (1) an incomplete outcome needs one focused follow-up; (2) a failed outcome needs concrete recovery; (3) a completed child is intentionally receiving a distinct next task; or (4) an applicable instruction or the user explicitly requires continuing that child. Never call while the child is running or its callback is pending. This tool is not for waiting, status checks, polling, reminders, duplicate instructions, or redoing delegated work. Address the exact instance by stable id. accepted=true means the idle child's next turn started, never completed. accepted=false with duplicate, already_sent, or callback_pending means no new dispatch; do not retry because the existing callback arrives automatically. After every result, inspect accepted, action, callback_action, must_wait_for_callback, and instruction. A pending result arrives automatically at a provider boundary of the active parent or in a callback continuation turn. Continue only work already planned before dispatch that is outside the delegated task and independent of its callback. If none remains, end the turn immediately without assistant content or another tool call; do not narrate waiting or call a response or delivery tool."
	}
	return Tool{Definition: agentruntime.ToolDefinition{Name: name, Description: description, InputSchema: mustRawToolSchema(schema)}, Handler: handler}
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

// subagentRoutingSummary exposes only the lifecycle fields needed to address a
// child. Outcome payloads remain callback-only until the callback is consumed.
type subagentRoutingSummary struct {
	ID             string                 `json:"id"`
	DisplayName    string                 `json:"display_name"`
	Label          string                 `json:"label,omitempty"`
	DefinitionName string                 `json:"definition_name"`
	Status         storage.SubagentStatus `json:"status"`
	CurrentTurnID  string                 `json:"current_turn_id,omitempty"`
	QueuedMessages int                    `json:"queued_messages"`
}

func summarizeSubagentRouting(record storage.Subagent) subagentRoutingSummary {
	return subagentRoutingSummary{
		ID: record.ID, DisplayName: record.DisplayName, Label: record.Label,
		DefinitionName: record.DefinitionName, Status: record.Status,
		CurrentTurnID: record.CurrentTurnID, QueuedMessages: len(record.Pending),
	}
}

func summarizeSubagentsRouting(records []storage.Subagent) []subagentRoutingSummary {
	result := make([]subagentRoutingSummary, len(records))
	for index, record := range records {
		result[index] = summarizeSubagentRouting(record)
	}
	return result
}

// SubagentStatusResult is a small, transcript-free status projection.
type SubagentStatusResult struct {
	Action          string              `json:"action"`
	Subagent        SubagentToolSummary `json:"subagent"`
	ActivitySummary string              `json:"activity_summary"`
	ResultReady     bool                `json:"result_ready"`
	Instruction     string              `json:"instruction"`
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

type startSubagentToolInput struct {
	Name                  string `json:"name"`
	Message               string `json:"message"`
	Label                 string `json:"label"`
	NewInstance           bool   `json:"new_instance"`
	ContinueAfterDispatch *bool  `json:"continue_after_dispatch"`
}

func (bridge *SubagentToolBridge) start(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input startSubagentToolInput
	if err := decodeSubagentTool(arguments, &input); err != nil {
		return nil, err
	}
	if input.ContinueAfterDispatch == nil {
		return nil, errors.New("continue_after_dispatch is required")
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
			Action       SubagentStartAction      `json:"action"`
			Candidates   []subagentRoutingSummary `json:"candidates"`
			Accepted     bool                     `json:"accepted"`
			Deduplicated bool                     `json:"deduplicated"`
			Asynchronous bool                     `json:"asynchronous"`
			Callback     string                   `json:"callback_action"`
			MustWait     bool                     `json:"must_wait_for_callback"`
			TurnBehavior string                   `json:"turn_behavior"`
			TurnAction   string                   `json:"turn_action"`
			NextAction   string                   `json:"next_action"`
		}{result.Action, summarizeSubagentsRouting(result.Candidates), false, false, false, "none", false, "continue_turn", "continue_selection_required", "No dispatch occurred and no callback will be created. continue_after_dispatch does not apply. Ask the user which friendly display_name to continue with. Do not choose for them and do not create another child. After they answer, call send_subagent_message with that candidate's id."})
	}
	record := result.Subagent
	callbackAction := "automatic"
	nextAction := subagentStartInstruction("Accepted. The result will arrive automatically later.", *input.ContinueAfterDispatch)
	if result.DispatchAction == SubagentSendDuplicate || result.DispatchAction == SubagentSendAlreadySent {
		callbackAction = "automatic_existing"
		nextAction = subagentStartInstruction("Not accepted. The existing result will arrive automatically later.", *input.ContinueAfterDispatch)
	} else if result.DispatchAction == SubagentSendCallbackPending {
		callbackAction = "automatic_existing"
		nextAction = subagentStartInstruction("Not accepted. The pending result will arrive automatically later.", *input.ContinueAfterDispatch)
	}
	turnAction := "continue_independent_work"
	if !*input.ContinueAfterDispatch {
		turnAction = "end_turn_wait_for_callback"
		if err := RequestEndTurn(ctx); err != nil {
			return nil, err
		}
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
		Accepted       bool                   `json:"accepted"`
		Deduplicated   bool                   `json:"deduplicated"`
		Asynchronous   bool                   `json:"asynchronous"`
		Callback       string                 `json:"callback_action"`
		MustWait       bool                   `json:"must_wait_for_callback"`
		TurnAction     string                 `json:"turn_action"`
		NextAction     string                 `json:"next_action"`
	}{record.ID, record.DisplayName, record.SessionID, record.CurrentTurnID, record.Status, result.Action, result.DispatchAction, result.Action == SubagentStartReused, result.Accepted, result.Deduplicated, true, callbackAction, true, turnAction, nextAction})
}

func subagentStartInstruction(prefix string, continueAfterDispatch bool) string {
	if continueAfterDispatch {
		return prefix + " Continue now with only the already-planned parent work outside the delegated task that justified continue_after_dispatch=true. Do not invent work, poll, or narrate waiting."
	}
	return prefix + " The current turn will end automatically after this successful tool batch. Do not generate assistant content or call another tool while waiting for the callback."
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
	snapshot, err := controller.StatusFromParentTurn(ctx, invocation.SessionID, invocation.TurnID, input.ID)
	if err != nil {
		return nil, err
	}
	action := "snapshot"
	instruction := "Use this single lifecycle snapshot to answer the user's explicit status question. Do not call subagent_status again in this parent turn and do not use it to wait; the callback arrives automatically at a safe provider boundary or in a continuation turn."
	if snapshot.Repeated {
		action = "already_checked"
		instruction = "This is the cached snapshot already returned for this child in this parent turn. No new status check occurred. Stop polling and end the turn or continue independent work; the callback arrives automatically."
	}
	record := snapshot.Subagent
	return json.Marshal(SubagentStatusResult{
		Action: action, Subagent: summarizeSubagent(record), ActivitySummary: summarizeSubagentActivity(record),
		ResultReady: record.Status == storage.SubagentStatusIdle && record.LastTurnOutcome == storage.SubagentTurnCompleted,
		Instruction: instruction,
	})
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
	callbackAction := "automatic"
	instruction := subagentSendAcceptedInstruction
	if !result.Accepted {
		callbackAction = "automatic_existing"
		if result.Action == SubagentSendCallbackPending {
			instruction = "Not accepted. The pending result will arrive automatically later. " + subagentWaitInstruction
		} else {
			instruction = "Not accepted. The existing result will arrive automatically later. " + subagentWaitInstruction
		}
	}
	return json.Marshal(struct {
		Action       SubagentSendAction     `json:"action"`
		Accepted     bool                   `json:"accepted"`
		Deduplicated bool                   `json:"deduplicated"`
		Subagent     subagentRoutingSummary `json:"subagent"`
		Callback     string                 `json:"callback_action"`
		MustWait     bool                   `json:"must_wait_for_callback"`
		Instruction  string                 `json:"instruction"`
	}{result.Action, result.Accepted, result.Deduplicated, summarizeSubagentRouting(result.Subagent), callbackAction, true, instruction})
}
