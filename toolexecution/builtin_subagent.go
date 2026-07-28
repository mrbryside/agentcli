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
	Start(context.Context, string, string, string, string, string) (storage.Subagent, error)
	List(context.Context, string, bool) ([]storage.Subagent, error)
	StatusFromParentTurn(context.Context, string, string, string) (SubagentStatusSnapshot, error)
	SendFromParentTurn(context.Context, string, string, string, string) (SubagentSendResult, error)
}

// SubagentStartAction describes a model-facing child creation.
type SubagentStartAction string

const (
	SubagentStartCreated SubagentStartAction = "created"
)

// SubagentSendAction describes how one parent-turn message was handled.
type SubagentSendAction string

const (
	SubagentSendStarted           SubagentSendAction = "started"
	SubagentSendQueued            SubagentSendAction = "queued"
	SubagentSendDuplicate         SubagentSendAction = "duplicate"
	SubagentSendAlreadySent       SubagentSendAction = "already_sent"
	SubagentSendCallbackPending   SubagentSendAction = "callback_pending"
	SubagentSendChildCompleted    SubagentSendAction = "child_completed"
	SubagentSendRecoveryExhausted SubagentSendAction = "recovery_exhausted"
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
		bridge.tool(StartSubagentToolName, "", `{"type":"object","properties":{"name":{"type":"string","minLength":1,"description":"Exact configured type name from available_subagents, selected because its description directly matches the focused delegated task or an applicable instruction explicitly requires it. This is not a child ID or display_name."},"message":{"type":"string","minLength":1,"description":"Self-contained focused delegated task including relevant context, constraints, and the result expected from the new child. Never use this field to ask for status, send a reminder, chase running work, or continue an existing child."},"label":{"type":"string","minLength":1,"maxLength":120,"description":"Optional short UI label for this new delegated task. Do not put instructions here."},"continue_after_dispatch":{"type":"boolean","description":"Controls only whether the parent continues immediately; it never controls subagent concurrency. Set false when no specific independent parent work remains after handing off this batch. The parent stops, the subagents keep running, and the runtime resumes the parent when a result is ready. Multiple subagents started together with false still run in parallel. Set true only for specific independent parent work already planned before dispatch that must run immediately. For multiple calls in one tool batch, use the same value on every call: all false when no parent work remains, or all true only for that planned independent work. Never mix values."}},"required":["name","message","continue_after_dispatch"],"additionalProperties":false}`, bridge.start),
		bridge.tool(SendSubagentMessageToolName, "", `{"type":"object","properties":{"subagent_id":{"type":"string","minLength":1,"description":"ID of an idle incomplete or failed child whose latest result was already delivered and consumed. Never use a running, completed, or closed child, a definition name, or display_name."},"message":{"type":"string","minLength":1,"description":"One self-contained focused follow-up for an incomplete child or recovery instruction for a failed child. Do not send unrelated new work, status checks, reminders, waiting requests, or repeat already-delegated work."},"continue_after_dispatch":{"type":"boolean","description":"Controls only whether the parent continues immediately after this follow-up. Set false when no specific independent parent work remains; the parent stops and the runtime resumes it when the subagent result is ready. Set true only for specific independent parent work already planned before dispatch that must run immediately. This choice never permits another message to the child while its result is outstanding. A duplicate, already_sent, or callback_pending result ends the successful batch regardless of this value."}},"required":["subagent_id","message","continue_after_dispatch"],"additionalProperties":false}`, bridge.send),
	}
}

func (bridge *SubagentToolBridge) tool(name, description, schema string, handler Handler) Tool {
	switch name {
	case StartSubagentToolName:
		description = "Create a new configured subagent for one focused assignment after a valid delegation trigger. Every successful call creates a separately addressed child; this tool never reuses or continues an existing child. To continue a specific incomplete or failed child, wait until it is idle and its latest result has been delivered and consumed, then use send_subagent_message with its stable id. Never reuse a completed child. This is not a status, reminder, or follow-up tool for running work: wait for its automatic result before interacting with that child again. Valid triggers are: (1) a definition description directly matches the focused task and delegation materially helps through specialized independent work, substantial context isolation, or useful parallelism; or (2) an applicable instruction or the user explicitly requires delegation or that subagent type. Topic overlap, discovery-only questions, and simple self-contained work do not trigger this tool by themselves; an applicable explicit requirement remains a valid trigger. Select the exact configured type from available_subagents; its description is selection metadata, not proof that work started. Before calling, explicitly choose continue_after_dispatch, which controls only the parent turn and never subagent concurrency. Set it to false when no specific independent parent work remains after handing off the batch; the parent stops after the whole successful tool batch, the subagents keep running, and the runtime resumes the parent when a result is ready. Multiple subagents started together with false still run in parallel. Set it to true only when specific independent parent work was already planned and must continue immediately; never invent work to justify true. For multiple start_subagent calls in one tool batch, use the same value on all calls: all false when no parent work remains, or all true only for that planned independent work. Never mix values because any false call that accepts work ends the successful batch. Prefer one child at a time; ordinary lookup or research should assess one result before starting another. Multiple starts in one response are only for genuinely independent comparison or parallel work. accepted=true means dispatched, never completed. The result arrives automatically later. Never call a tool, search, poll, emit assistant content, or call a response or delivery tool merely to simulate waiting."
	case SendSubagentMessageToolName:
		description = "Send one focused message to an existing idle incomplete or failed child only, after its latest result has been delivered and consumed. Valid triggers are: (1) an incomplete outcome needs one focused follow-up; (2) a failed outcome needs concrete recovery; or (3) an applicable instruction or the user explicitly requires one of those continuations. Never call while the child is running or its result is outstanding. Never reuse a completed child; deliver its result and let it close automatically. Use start_subagent only when genuinely new work independently requires a new child. This tool is not for waiting, status checks, polling, reminders, duplicate instructions, or redoing delegated work. It is also not for unrelated new work. Address the exact instance by stable id. Before calling, explicitly choose continue_after_dispatch, which controls only the parent turn. Set it to false when no specific independent parent work remains; an accepted result ends the current successful tool batch automatically while the child keeps running. Set it to true only when specific independent parent work was already planned and must continue immediately; never invent work to justify true. accepted=true means the idle child's next turn started, never completed. accepted=false with duplicate, already_sent, or callback_pending means no new dispatch; the current successful tool batch ends automatically regardless of continue_after_dispatch, so do not retry because the existing result arrives automatically. child_completed also means no dispatch; continue to deliver its completed result and do not message that child again. recovery_exhausted means the same failed child and normalized failure already received one recovery dispatch in this response; continue to report the terminal failure and do not retry. After every result, inspect accepted, action, callback_action, must_wait_for_callback, turn_action, and instruction. The subagent result arrives automatically later. With continue_after_dispatch=true, continue only the specific independent parent work planned before dispatch, then stop and wait for the subagent result. Never call a tool, search, poll, emit assistant content, or call a response or delivery tool merely to simulate waiting."
	}
	tool := Tool{Definition: agentruntime.ToolDefinition{Name: name, Description: description, InputSchema: mustRawToolSchema(schema)}, Handler: handler}
	if name == SendSubagentMessageToolName {
		tool.resultTurnBehavior = sendSubagentMessageTurnBehavior
	}
	return tool
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
	record, err := controller.Start(ctx, invocation.SessionID, invocation.TurnID, input.Name, input.Message, input.Label)
	if err != nil {
		return nil, err
	}
	callbackAction := "automatic"
	nextAction := subagentStartInstruction("Accepted. The result will arrive automatically later.", *input.ContinueAfterDispatch)
	turnAction := "continue_independent_work"
	if !*input.ContinueAfterDispatch {
		turnAction = "end_turn_wait_for_callback"
		if err := RequestEndTurn(ctx); err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		SubagentID   string                 `json:"subagent_id"`
		DisplayName  string                 `json:"display_name"`
		SessionID    string                 `json:"session_id"`
		TurnID       string                 `json:"turn_id"`
		Status       storage.SubagentStatus `json:"status"`
		Action       SubagentStartAction    `json:"action"`
		Accepted     bool                   `json:"accepted"`
		Asynchronous bool                   `json:"asynchronous"`
		Callback     string                 `json:"callback_action"`
		MustWait     bool                   `json:"must_wait_for_callback"`
		TurnAction   string                 `json:"turn_action"`
		NextAction   string                 `json:"next_action"`
	}{record.ID, record.DisplayName, record.SessionID, record.CurrentTurnID, record.Status, SubagentStartCreated, true, true, callbackAction, true, turnAction, nextAction})
}

func subagentStartInstruction(prefix string, continueAfterDispatch bool) string {
	if continueAfterDispatch {
		return prefix + " Continue now with only the specific independent parent work planned before dispatch that justified continue_after_dispatch=true. When that work is finished, stop and wait for the subagent result. Never call a tool, search, poll, or emit assistant content merely to simulate waiting."
	}
	return prefix + " Stop now and wait for the subagent result. The current turn ends automatically after this successful tool batch while the subagent keeps running; the runtime resumes the parent when the result is ready. Do not generate assistant content or call another tool."
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

type sendSubagentMessageToolInput struct {
	ID                    string `json:"subagent_id"`
	Message               string `json:"message"`
	ContinueAfterDispatch *bool  `json:"continue_after_dispatch"`
}

type sendSubagentMessageToolOutput struct {
	Action       SubagentSendAction     `json:"action"`
	Accepted     bool                   `json:"accepted"`
	Deduplicated bool                   `json:"deduplicated"`
	Subagent     subagentRoutingSummary `json:"subagent"`
	Callback     string                 `json:"callback_action"`
	MustWait     bool                   `json:"must_wait_for_callback"`
	TurnAction   string                 `json:"turn_action"`
	Instruction  string                 `json:"instruction"`
}

func (bridge *SubagentToolBridge) send(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input sendSubagentMessageToolInput
	if err := decodeSubagentTool(arguments, &input); err != nil {
		return nil, err
	}
	if input.ContinueAfterDispatch == nil {
		return nil, errors.New("continue_after_dispatch is required")
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
	mustWait := true
	turnAction := "continue_independent_work"
	instruction := "Accepted. The result will arrive automatically later. Continue now with only the specific independent parent work planned before dispatch that justified continue_after_dispatch=true. When that work is finished, stop and wait for the subagent result. Never call a tool, search, poll, resend, or emit assistant content merely to simulate waiting."
	if result.Accepted && !*input.ContinueAfterDispatch {
		turnAction = "end_turn_wait_for_callback"
		instruction = "Accepted. Stop now and wait for the subagent result. The current successful tool batch ends automatically while the child keeps running; the runtime resumes the parent when the result is ready. Do not generate assistant content or call another tool."
	} else if !result.Accepted {
		callbackAction = "automatic_existing"
		switch result.Action {
		case SubagentSendChildCompleted:
			callbackAction = "none"
			mustWait = false
			turnAction = "continue_to_deliver_completed_result"
			instruction = "Not accepted. This child already completed and must not be reused. Deliver its completed result. Start a new child only if genuinely new work independently requires delegation."
		case SubagentSendRecoveryExhausted:
			callbackAction = "none"
			mustWait = false
			turnAction = "continue_to_report_terminal_failure"
			instruction = "Not accepted. This failed child already received the allowed recovery dispatch for the same normalized failure in this response. Report the terminal failure and do not retry this child or dispatch equivalent recovery work."
		default:
			turnAction = "end_turn_wait_for_callback"
			instruction = "Not accepted. No new work was dispatched. Stop now and wait for the existing subagent result. The current successful tool batch ends automatically, and the runtime resumes the parent when the result is ready. Do not retry, call another tool, or generate assistant content."
		}
	}
	return json.Marshal(sendSubagentMessageToolOutput{
		Action: result.Action, Accepted: result.Accepted, Deduplicated: result.Deduplicated,
		Subagent: summarizeSubagentRouting(result.Subagent), Callback: callbackAction,
		MustWait: mustWait, TurnAction: turnAction, Instruction: instruction,
	})
}

func sendSubagentMessageTurnBehavior(arguments, output json.RawMessage) agentruntime.ToolTurnBehavior {
	var input sendSubagentMessageToolInput
	if err := json.Unmarshal(arguments, &input); err != nil {
		return agentruntime.ToolTurnContinue
	}
	var result sendSubagentMessageToolOutput
	if err := json.Unmarshal(output, &result); err != nil {
		return agentruntime.ToolTurnContinue
	}
	if result.Accepted {
		if input.ContinueAfterDispatch != nil && !*input.ContinueAfterDispatch {
			return agentruntime.ToolTurnEndOnSuccess
		}
		return agentruntime.ToolTurnContinue
	}
	switch result.Action {
	case SubagentSendDuplicate, SubagentSendAlreadySent, SubagentSendCallbackPending:
		return agentruntime.ToolTurnEndOnSuccess
	default:
		return agentruntime.ToolTurnContinue
	}
}
