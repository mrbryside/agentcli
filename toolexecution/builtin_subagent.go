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
	StatusFromMainAgentTurn(context.Context, string, string, string) (SubagentStatusSnapshot, error)
	SendFromMainAgentTurn(context.Context, string, string, string, string) (SubagentSendResult, error)
}

// SubagentStartAction describes a model-facing subagent creation.
type SubagentStartAction string

const (
	SubagentStartCreated SubagentStartAction = "created"
)

// SubagentSendAction describes how one main agent-turn message was handled.
type SubagentSendAction string

const (
	SubagentSendStarted           SubagentSendAction = "started"
	SubagentSendQueued            SubagentSendAction = "queued"
	SubagentSendDuplicate         SubagentSendAction = "duplicate"
	SubagentSendAlreadySent       SubagentSendAction = "already_sent"
	SubagentSendResultPending     SubagentSendAction = "result_pending"
	SubagentSendCompleted         SubagentSendAction = "subagent_completed"
	SubagentSendRecoveryExhausted SubagentSendAction = "recovery_exhausted"
)

// SubagentSendResult exposes the enforced main agent-turn idempotency decision.
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
	Subagent             storage.Subagent
	PreviousStatus       storage.SubagentStatus
	PreviousResultStatus storage.SubagentResultStatus
	DroppedMessages      int
	Interrupted          bool
}

// SubagentStatusSnapshot is one cached lifecycle observation for a subagent in a
// main agent turn. Repeated reads return the original record rather than polling.
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

// Tools returns the static main-agent-only subagent built-in catalog.
// Subagents never register this bridge.
func (bridge *SubagentToolBridge) Tools() []Tool {
	return []Tool{
		bridge.tool(StartSubagentToolName, "", `{"type":"object","properties":{"name":{"type":"string","minLength":1,"description":"Exact subagent type name from available_subagents."},"message":{"type":"string","minLength":1,"description":"Self-contained focused assignment with the context, constraints, and expected result."},"label":{"type":"string","minLength":1,"maxLength":120,"description":"Optional short display label. Do not put instructions here."},"continue_main_agent":{"type":"boolean","description":"Set false when the main agent should stop after starting this work. Set true only when specific independent main-agent work was already planned and must continue now. This does not control parallel subagents. Multiple starts in one tool batch must use the same value."}},"required":["name","message","continue_main_agent"],"additionalProperties":false}`, bridge.start),
		bridge.tool(SendSubagentMessageToolName, "", `{"type":"object","properties":{"subagent_id":{"type":"string","minLength":1,"description":"Exact id of an idle incomplete or failed subagent whose latest result was already delivered."},"message":{"type":"string","minLength":1,"description":"One focused follow-up for an incomplete result or one concrete recovery instruction for a failed result."},"continue_main_agent":{"type":"boolean","description":"Set false when the main agent should stop after sending this follow-up. Set true only when specific independent main-agent work was already planned and must continue now."}},"required":["subagent_id","message","continue_main_agent"],"additionalProperties":false}`, bridge.send),
	}
}

func (bridge *SubagentToolBridge) tool(name, description, schema string, handler Handler) Tool {
	switch name {
	case StartSubagentToolName:
		description = "Start one new subagent for one focused assignment. Use the exact type name from available_subagents. This tool never continues an existing subagent; use send_subagent_message for a delivered incomplete or failed result. Prefer one subagent unless assignments are independent and useful in parallel. Read accepted, action, main_agent_action, and instruction in the result. accepted=true means work started, not completed. The final result arrives later as <subagent_result>. Never use this tool for status checks, reminders, polling, or waiting."
	case SendSubagentMessageToolName:
		description = "Send one focused follow-up to the exact idle subagent named by subagent_id. Use only after an incomplete or failed <subagent_result> was delivered. Never send to running or completed work, and never use this tool for status checks, reminders, polling, waiting, unrelated work, or repeated instructions. Read accepted, action, main_agent_action, and instruction in the result. accepted=true means the next subagent turn started, not completed. A later <subagent_result> contains the result."
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

// SubagentToolSummary is the stable JSON-facing subagent state projection.
type SubagentToolSummary struct {
	ID                    string                       `json:"id"`
	DisplayName           string                       `json:"display_name"`
	Label                 string                       `json:"label,omitempty"`
	SubagentSessionID     string                       `json:"subagent_session_id"`
	DefinitionName        string                       `json:"definition_name"`
	Provider              string                       `json:"provider"`
	Model                 string                       `json:"model"`
	Status                storage.SubagentStatus       `json:"status"`
	CurrentSubagentTurnID string                       `json:"current_subagent_turn_id,omitempty"`
	LastSubagentTurnID    string                       `json:"last_subagent_turn_id,omitempty"`
	LastResultError       string                       `json:"last_result_error,omitempty"`
	LastResultStatus      storage.SubagentResultStatus `json:"last_result_status,omitempty"`
	LastResultSummary     string                       `json:"last_result_summary,omitempty"`
	LastResultNextStep    string                       `json:"last_result_next_step,omitempty"`
	Version               uint64                       `json:"version"`
	QueuedMessages        int                          `json:"queued_messages"`
}

// subagentRoutingSummary exposes only the lifecycle fields needed to address a
// subagent. Result payloads remain result-only until the result is consumed.
type subagentRoutingSummary struct {
	ID                    string                 `json:"id"`
	DisplayName           string                 `json:"display_name"`
	Label                 string                 `json:"label,omitempty"`
	DefinitionName        string                 `json:"definition_name"`
	Status                storage.SubagentStatus `json:"status"`
	CurrentSubagentTurnID string                 `json:"current_subagent_turn_id,omitempty"`
	QueuedMessages        int                    `json:"queued_messages"`
}

func summarizeSubagentRouting(record storage.Subagent) subagentRoutingSummary {
	return subagentRoutingSummary{
		ID: record.ID, DisplayName: record.DisplayName, Label: record.Label,
		DefinitionName: record.DefinitionName, Status: record.Status,
		CurrentSubagentTurnID: record.CurrentSubagentTurnID, QueuedMessages: len(record.Pending),
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
	return SubagentToolSummary{ID: record.ID, DisplayName: record.DisplayName, Label: record.Label, SubagentSessionID: record.SubagentSessionID, DefinitionName: record.DefinitionName, Provider: record.Provider, Model: record.Model, Status: record.Status, CurrentSubagentTurnID: record.CurrentSubagentTurnID, LastSubagentTurnID: record.LastSubagentTurnID, LastResultError: record.LastResultError, LastResultStatus: record.LastResultStatus, LastResultSummary: record.LastResultSummary, LastResultNextStep: record.LastResultNextStep, Version: record.Version, QueuedMessages: len(record.Pending)}
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
	case record.LastResultError != "":
		activity = "Last turn failed: " + record.LastResultError
	case record.Status == storage.SubagentStatusIdle && record.LastResultStatus == storage.SubagentResultIncomplete:
		activity = "Incomplete: " + task + "; next: " + record.LastResultNextStep
	case record.Status == storage.SubagentStatusIdle && record.LastSubagentTurnID != "":
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
	Name              string `json:"name"`
	Message           string `json:"message"`
	Label             string `json:"label"`
	ContinueMainAgent *bool  `json:"continue_main_agent"`
}

func (bridge *SubagentToolBridge) start(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input startSubagentToolInput
	if err := decodeSubagentTool(arguments, &input); err != nil {
		return nil, err
	}
	if input.ContinueMainAgent == nil {
		return nil, errors.New("continue_main_agent is required")
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
	resultDelivery := "automatic"
	instruction := subagentStartInstruction("Accepted. The subagent result will arrive automatically.", *input.ContinueMainAgent)
	mainAgentAction := "continue_independent_work"
	if !*input.ContinueMainAgent {
		mainAgentAction = "stop_and_wait"
		if err := RequestEndTurn(ctx); err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		SubagentID      string                 `json:"subagent_id"`
		DisplayName     string                 `json:"display_name"`
		Status          storage.SubagentStatus `json:"status"`
		Action          SubagentStartAction    `json:"action"`
		Accepted        bool                   `json:"accepted"`
		ResultDelivery  string                 `json:"result_delivery"`
		MainAgentAction string                 `json:"main_agent_action"`
		Instruction     string                 `json:"instruction"`
	}{record.ID, record.DisplayName, record.Status, SubagentStartCreated, true, resultDelivery, mainAgentAction, instruction})
}

func subagentStartInstruction(prefix string, continueMainAgent bool) string {
	if continueMainAgent {
		return prefix + " Continue only the specific independent main-agent work already planned. When it is finished, stop. Never call a tool or emit assistant content merely to simulate waiting."
	}
	return prefix + " Stop now. The current turn ends automatically while the subagent keeps running. Do not generate assistant content or call another tool."
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
	snapshot, err := controller.StatusFromMainAgentTurn(ctx, invocation.SessionID, invocation.TurnID, input.ID)
	if err != nil {
		return nil, err
	}
	action := "snapshot"
	instruction := "Use this single status snapshot to answer the user's explicit status question. Do not call subagent_status again in this main-agent turn and do not use it to wait; the subagent result arrives automatically."
	if snapshot.Repeated {
		action = "already_checked"
		instruction = "This is the same snapshot already returned for this subagent in this main-agent turn. No new status check occurred. Stop polling and end the turn or continue independent work; the subagent result arrives automatically."
	}
	record := snapshot.Subagent
	return json.Marshal(SubagentStatusResult{
		Action: action, Subagent: summarizeSubagent(record), ActivitySummary: summarizeSubagentActivity(record),
		ResultReady: record.Status == storage.SubagentStatusIdle && record.LastResultStatus == storage.SubagentResultCompleted,
		Instruction: instruction,
	})
}

type sendSubagentMessageToolInput struct {
	ID                string `json:"subagent_id"`
	Message           string `json:"message"`
	ContinueMainAgent *bool  `json:"continue_main_agent"`
}

type sendSubagentMessageToolOutput struct {
	Action          SubagentSendAction     `json:"action"`
	Accepted        bool                   `json:"accepted"`
	Deduplicated    bool                   `json:"deduplicated"`
	Subagent        subagentRoutingSummary `json:"subagent"`
	ResultDelivery  string                 `json:"result_delivery"`
	MainAgentAction string                 `json:"main_agent_action"`
	Instruction     string                 `json:"instruction"`
}

func (bridge *SubagentToolBridge) send(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input sendSubagentMessageToolInput
	if err := decodeSubagentTool(arguments, &input); err != nil {
		return nil, err
	}
	if input.ContinueMainAgent == nil {
		return nil, errors.New("continue_main_agent is required")
	}
	invocation, err := subagentInvocation(ctx, SendSubagentMessageToolName)
	if err != nil {
		return nil, err
	}
	controller, err := bridge.get()
	if err != nil {
		return nil, err
	}
	result, err := controller.SendFromMainAgentTurn(ctx, invocation.SessionID, invocation.TurnID, input.ID, input.Message)
	if err != nil {
		return nil, err
	}
	resultDelivery := "automatic"
	mainAgentAction := "continue_independent_work"
	instruction := "Accepted. The subagent result will arrive automatically. Continue only the specific independent main-agent work already planned, then stop. Never call a tool or emit assistant content merely to simulate waiting."
	if result.Accepted && !*input.ContinueMainAgent {
		mainAgentAction = "stop_and_wait"
		instruction = "Accepted. Stop now. The current turn ends automatically while the subagent keeps running. Do not generate assistant content or call another tool."
	} else if !result.Accepted {
		resultDelivery = "existing"
		switch result.Action {
		case SubagentSendCompleted:
			resultDelivery = "none"
			mainAgentAction = "deliver_completed_result"
			instruction = "Not accepted. This subagent already completed and must not be reused. Deliver its completed result. Start a new subagent only if genuinely new work requires one."
		case SubagentSendRecoveryExhausted:
			resultDelivery = "none"
			mainAgentAction = "report_terminal_failure"
			instruction = "Not accepted. This failed subagent already received the allowed recovery message for the same failure in this user response. Report the terminal failure and do not retry this subagent or send equivalent recovery work."
		default:
			mainAgentAction = "stop_and_wait"
			instruction = "Not accepted. No new work started. Stop now; the existing subagent result will arrive automatically. Do not retry, call another tool, or generate assistant content."
		}
	}
	return json.Marshal(sendSubagentMessageToolOutput{
		Action: result.Action, Accepted: result.Accepted, Deduplicated: result.Deduplicated,
		Subagent: summarizeSubagentRouting(result.Subagent), ResultDelivery: resultDelivery,
		MainAgentAction: mainAgentAction, Instruction: instruction,
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
		if input.ContinueMainAgent != nil && !*input.ContinueMainAgent {
			return agentruntime.ToolTurnEndOnSuccess
		}
		return agentruntime.ToolTurnContinue
	}
	switch result.Action {
	case SubagentSendDuplicate, SubagentSendAlreadySent, SubagentSendResultPending:
		return agentruntime.ToolTurnEndOnSuccess
	default:
		return agentruntime.ToolTurnContinue
	}
}
