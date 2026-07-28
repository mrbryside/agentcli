package agentcli

import (
	"encoding/json"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
)

// RunStatusQueued is an HTTP-server lifecycle state used before the strict
// single-session runtime admits an accepted turn.
const RunStatusQueued agentruntime.RunStatus = "queued"

// ServerTurnSource identifies the source of one session event. Main-agent
// turns use user; task and host-management events are published without
// creating another main-agent turn.
type ServerTurnSource string

const (
	ServerTurnSourceUser                 ServerTurnSource = "user"
	ServerTurnSourceTask                 ServerTurnSource = "task"
	ServerTurnSourceSubagentConfirmation ServerTurnSource = "subagent_confirmation"
	ServerTurnSourceSubagentPermission   ServerTurnSource = "subagent_permission"
	ServerTurnSourceSubagentLifecycle    ServerTurnSource = "subagent_lifecycle"
)

// SessionActivityType identifies server lifecycle records around ordinary
// runtime events. Runtime events use SessionActivityTurnEvent and retain their
// original type in RuntimeEvent.Type.
type SessionActivityType string

const (
	SessionActivityTurnQueued           SessionActivityType = "turn_queued"
	SessionActivityTurnAdmitted         SessionActivityType = "turn_admitted"
	SessionActivityTurnCancelled        SessionActivityType = "turn_cancelled"
	SessionActivityTurnRejected         SessionActivityType = "turn_rejected"
	SessionActivityTurnEvent            SessionActivityType = "turn_event"
	SessionActivityScopeEvent           SessionActivityType = "scope_event"
	SessionActivitySubagentConfirmation SessionActivityType = "subagent_confirmation"
	SessionActivitySubagentPermission   SessionActivityType = "subagent_permission"
	SessionActivitySubagentClosed       SessionActivityType = "subagent_closed"
	SessionActivityTaskCompleted        SessionActivityType = "task_completed"
)

// StartTurnRequest is the JSON body accepted by POST /v1/sessions/{id}/turns.
type StartTurnRequest struct {
	Message string `json:"message" validate:"required"` // User message for the new turn.
	TurnID  string `json:"turn_id,omitempty"`           // Optional caller-defined idempotency identity.
}

type StartTurnResponse struct {
	SessionID        string                 `json:"session_id"`
	TurnID           string                 `json:"turn_id"`
	Status           agentruntime.RunStatus `json:"status"`
	QueuePosition    int                    `json:"queue_position,omitempty"`
	TurnURL          string                 `json:"turn_url"`
	EventsURL        string                 `json:"events_url"`
	SessionEventsURL string                 `json:"session_events_url"`
	MessagesURL      string                 `json:"messages_url"`
}

type TurnResponse struct {
	SessionID     string                 `json:"session_id"`
	TurnID        string                 `json:"turn_id"`
	Status        agentruntime.RunStatus `json:"status"`
	QueuePosition int                    `json:"queue_position,omitempty"`
	Result        *RunResultResponse     `json:"result,omitempty"`
	Error         string                 `json:"error,omitempty"`
}

// TaskCompletedReference is the HTTP-safe view of a task completion. Metadata
// is application-only: it is exposed through system/session events but never
// becomes part of a provider-visible message.
type TaskCompletedReference struct {
	TaskID            string         `json:"task_id"`
	SubagentSessionID string         `json:"subagent_session_id"`
	SubagentTurnID    string         `json:"subagent_turn_id"`
	AgentName         string         `json:"agent"`
	State             TaskState      `json:"state"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

type SubagentConfirmationReference struct {
	Type              SubagentConfirmationEventType      `json:"type"`
	SubagentID        string                             `json:"subagent_id"`
	DisplayName       string                             `json:"display_name,omitempty"`
	DefinitionName    string                             `json:"definition_name"`
	SubagentSessionID string                             `json:"subagent_session_id"`
	SubagentTurnID    string                             `json:"subagent_turn_id"`
	Confirmation      *ConfirmationRequestResponse       `json:"confirmation,omitempty"`
	Decision          *ConfirmationDecisionResponseValue `json:"decision,omitempty"`
}

type PendingSubagentConfirmationsResponse struct {
	Confirmations []SubagentConfirmationReference `json:"confirmations"`
}

type SubagentPermissionReference struct {
	Type              SubagentPermissionEventType `json:"type"`
	SubagentID        string                      `json:"subagent_id"`
	DisplayName       string                      `json:"display_name,omitempty"`
	DefinitionName    string                      `json:"definition_name"`
	SubagentSessionID string                      `json:"subagent_session_id"`
	SubagentTurnID    string                      `json:"subagent_turn_id"`
	Permission        *PermissionRequestResponse  `json:"permission,omitempty"`
	Decision          *DecisionResponse           `json:"decision,omitempty"`
}

type PendingSubagentPermissionsResponse struct {
	Permissions []SubagentPermissionReference `json:"permissions"`
}

// SubagentClosedReference describes the subagent state removed by one successful
// explicit or automatic close.
type SubagentClosedReference struct {
	Subagent             SubagentResponse             `json:"subagent"`
	PreviousStatus       storage.SubagentStatus       `json:"previous_status"`
	PreviousResultStatus storage.SubagentResultStatus `json:"previous_result_status,omitempty"`
	DroppedMessages      int                          `json:"dropped_messages,omitempty"`
	Interrupted          bool                         `json:"interrupted,omitempty"`
	Automatic            bool                         `json:"automatic,omitempty"`
}

// SessionEventResponse is the session-wide SSE envelope. Cursor is monotonic
// across every main-agent turn in one session and is independent from the
// per-turn RuntimeEvent.Sequence cursor.
type SessionEventResponse struct {
	Cursor               uint64                         `json:"cursor"`
	Type                 SessionActivityType            `json:"type"`
	Source               ServerTurnSource               `json:"source"`
	SessionID            string                         `json:"session_id"`
	TurnID               string                         `json:"turn_id"`
	QueuePosition        int                            `json:"queue_position,omitempty"`
	TurnURL              string                         `json:"turn_url,omitempty"`
	EventsURL            string                         `json:"events_url,omitempty"`
	Error                string                         `json:"error,omitempty"`
	SubagentConfirmation *SubagentConfirmationReference `json:"subagent_confirmation,omitempty"`
	SubagentPermission   *SubagentPermissionReference   `json:"subagent_permission,omitempty"`
	SubagentClosed       *SubagentClosedReference       `json:"subagent_closed,omitempty"`
	TaskCompleted        *TaskCompletedReference        `json:"task_completed,omitempty"`
	RuntimeEvent         *EventResponse                 `json:"runtime_event,omitempty"`
	ScopeEvent           *ScopeEventResponse            `json:"scope_event,omitempty"`
}

// ScopeEventResponse is the HTTP-safe view of PreEndScope and
// EndScope. ScopeID is the originating human turn; TriggerTurnID is the final
// turn whose completion made the scope quiescent.
type ScopeEventResponse struct {
	Type          ScopeEventType `json:"type" swaggertype:"string" enums:"pre_end_scope,end_scope"`
	SessionID     string         `json:"session_id"`
	ScopeID       string         `json:"scope_id"`
	TriggerTurnID string         `json:"trigger_turn_id"`
	SubagentIDs   []string       `json:"subagent_ids"`
	ToolNames     []string       `json:"tool_names"`
	OccurredAt    time.Time      `json:"occurred_at"`
}

type InterruptRequest struct {
	Reason string `json:"reason,omitempty"`
}

type PermissionDecisionRequest struct {
	SessionID string                  `json:"session_id" validate:"required"`
	TurnID    string                  `json:"turn_id" validate:"required"`
	CallID    string                  `json:"call_id" validate:"required"`
	Decision  permission.DecisionType `json:"decision" validate:"required"`
}

type PermissionDecisionResponse struct {
	Decision DecisionResponse `json:"decision"`
}

type ConfirmationDecisionRequest struct {
	SessionID string              `json:"session_id" validate:"required"`
	TurnID    string              `json:"turn_id" validate:"required"`
	CallID    string              `json:"call_id" validate:"required"`
	Answer    confirmation.Answer `json:"answer" validate:"required"`
}

type ConfirmationDecisionResponse struct {
	Decision ConfirmationDecisionResponseValue `json:"decision"`
}

type SetPermissionModeRequest struct {
	Mode permission.Mode `json:"mode" validate:"required"`
}

type PermissionModeResponse struct {
	Previous permission.Mode `json:"previous,omitempty"`
	Mode     permission.Mode `json:"mode"`
}

type MessagesResponse struct {
	Messages []MessageResponse `json:"messages"`
}

// SubagentDefinitionResponse is host session-management discovery data.
// Instructions and local paths are intentionally omitted.
type SubagentDefinitionResponse struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	Skills      []string `json:"skills"`
	Tools       []string `json:"tools"`
}

type SubagentDefinitionsResponse struct {
	Definitions []SubagentDefinitionResponse `json:"definitions"`
}

// CreateSubagentRequest starts a host-managed subagent session from a project
// definition. MainAgentTurnID is optional for direct UI creation; the server
// assigns a synthetic ID when it is not supplied.
type CreateSubagentRequest struct {
	Name            string `json:"name" validate:"required"`
	Message         string `json:"message" validate:"required"`
	Label           string `json:"label,omitempty"`
	MainAgentTurnID string `json:"main_agent_turn_id,omitempty"`
}

type SendSubagentMessageRequest struct {
	Message string `json:"message" validate:"required"`
}

// SubagentResponse is host session-management data for one persisted
// subagent. Pending message content remains private to the manager mailbox.
type SubagentResponse struct {
	ID                    string                       `json:"id"`
	DisplayName           string                       `json:"display_name"`
	Label                 string                       `json:"label,omitempty"`
	MainAgentSessionID    string                       `json:"main_agent_session_id"`
	MainAgentTurnID       string                       `json:"main_agent_turn_id"`
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
	CreatedAt             time.Time                    `json:"created_at"`
	UpdatedAt             time.Time                    `json:"updated_at"`
	ClosedAt              *time.Time                   `json:"closed_at,omitempty"`
}

type SubagentsResponse struct {
	Subagents []SubagentResponse `json:"subagents"`
}

type SubagentMessagesResponse struct {
	Subagent SubagentResponse  `json:"subagent"`
	Messages []MessageResponse `json:"messages"`
}

type SubagentTurnResponse struct {
	Subagent SubagentResponse `json:"subagent"`
	Turn     TurnResponse     `json:"turn"`
}

type APIErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EventResponse is the stable JSON representation emitted over SSE.
type EventResponse struct {
	Sequence             uint64                             `json:"sequence"`
	SessionID            string                             `json:"session_id"`
	TurnID               string                             `json:"turn_id"`
	Type                 agentruntime.EventType             `json:"type"`
	Message              *MessageResponse                   `json:"message,omitempty"`
	ProviderEvent        *ProviderEventResponse             `json:"provider_event,omitempty"`
	ToolRequest          *ToolRequestResponse               `json:"tool_request,omitempty"`
	ToolResult           *ToolResultEnvelopeResponse        `json:"tool_result,omitempty"`
	Result               *RunResultResponse                 `json:"result,omitempty"`
	Error                string                             `json:"error,omitempty"`
	Reason               string                             `json:"reason,omitempty"`
	Permission           *PermissionRequestResponse         `json:"permission,omitempty"`
	Decision             *DecisionResponse                  `json:"decision,omitempty"`
	PermissionMode       *PermissionModeChangeResponse      `json:"permission_mode,omitempty"`
	Confirmation         *ConfirmationRequestResponse       `json:"confirmation,omitempty"`
	ConfirmationDecision *ConfirmationDecisionResponseValue `json:"confirmation_decision,omitempty"`
}

type MessageResponse struct {
	ID         string              `json:"id"`
	SessionID  string              `json:"session_id"`
	TurnID     string              `json:"turn_id"`
	Type       storage.MessageType `json:"type"`
	Content    string              `json:"content,omitempty"`
	Reasoning  string              `json:"reasoning,omitempty"`
	ToolCalls  []ToolCallResponse  `json:"tool_calls,omitempty"`
	ToolResult *ToolResultResponse `json:"tool_result,omitempty"`
	CreatedAt  time.Time           `json:"created_at"`
}

type ToolCallResponse struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResultResponse struct {
	CallID string                        `json:"call_id"`
	Name   string                        `json:"name"`
	Status agentruntime.ToolResultStatus `json:"status"`
	Output json.RawMessage               `json:"output,omitempty"`
	Error  string                        `json:"error,omitempty"`
	// TriggerSatisfied is present for successful EndResponseScope results.
	// False means an early runtime-owned skip; true means the final handler executed.
	TriggerSatisfied *bool `json:"trigger_satisfied,omitempty"`
}

type ToolRequestResponse struct {
	SessionID string           `json:"session_id"`
	TurnID    string           `json:"turn_id"`
	Call      ToolCallResponse `json:"call"`
}

type ToolResultEnvelopeResponse struct {
	SessionID    string                        `json:"session_id"`
	TurnID       string                        `json:"turn_id"`
	Result       ToolResultResponse            `json:"result"`
	TurnBehavior agentruntime.ToolTurnBehavior `json:"turn_behavior,omitempty"`
}

type ProviderEventResponse struct {
	Type         provider.EventType            `json:"type"`
	Content      string                        `json:"content,omitempty"`
	Reasoning    string                        `json:"reasoning,omitempty"`
	Tool         *ProviderToolEventResponse    `json:"tool,omitempty"`
	Error        string                        `json:"error,omitempty"`
	FinishReason string                        `json:"finish_reason,omitempty"`
	Payload      *ProviderEventPayloadResponse `json:"payload,omitempty"`
}

type ProviderToolEventResponse struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ProviderEventPayloadResponse is the stable HTTP representation of the
// provider package's documented terminal payloads.
type ProviderEventPayloadResponse struct {
	Result *ProviderStreamResultResponse `json:"result,omitempty"`
	Error  string                        `json:"error,omitempty"`
}

type ProviderStreamResultResponse struct {
	Content        string                              `json:"content,omitempty"`
	Reasoning      string                              `json:"reasoning,omitempty"`
	CompletedTools []ProviderCompletedToolCallResponse `json:"completed_tools,omitempty"`
	Finished       bool                                `json:"finished"`
}

type ProviderCompletedToolCallResponse struct {
	ID        string         `json:"id"`
	Type      string         `json:"type,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type RunResultResponse struct {
	SessionID   string               `json:"session_id"`
	TurnID      string               `json:"turn_id"`
	Content     string               `json:"content,omitempty"`
	Reasoning   string               `json:"reasoning,omitempty"`
	ToolResults []ToolResultResponse `json:"tool_results,omitempty"`
	Steps       int                  `json:"steps"`
	Finished    bool                 `json:"finished"`
}

type PermissionRequestResponse struct {
	ID        permission.ID       `json:"id"`
	SessionID string              `json:"session_id"`
	TurnID    string              `json:"turn_id"`
	CallID    string              `json:"call_id"`
	ToolName  string              `json:"tool_name"`
	Details   string              `json:"details,omitempty"`
	Reason    string              `json:"reason,omitempty"`
	Risk      permission.Risk     `json:"risk"`
	Actions   []permission.Action `json:"actions"`
	CreatedAt time.Time           `json:"created_at"`
	ExpiresAt *time.Time          `json:"expires_at,omitempty"`
}

type DecisionResponse struct {
	PermissionID permission.ID           `json:"permission_id"`
	SessionID    string                  `json:"session_id"`
	TurnID       string                  `json:"turn_id"`
	CallID       string                  `json:"call_id"`
	Type         permission.DecisionType `json:"type"`
}

type ConfirmationRequestResponse struct {
	ID        confirmation.ID `json:"id"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id"`
	CallID    string          `json:"call_id"`
	ToolName  string          `json:"tool_name"`
	Title     string          `json:"title,omitempty"`
	Message   string          `json:"message"`
	Details   string          `json:"details,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt *time.Time      `json:"expires_at,omitempty"`
}

type ConfirmationDecisionResponseValue struct {
	ConfirmationID confirmation.ID     `json:"confirmation_id"`
	SessionID      string              `json:"session_id"`
	TurnID         string              `json:"turn_id"`
	CallID         string              `json:"call_id"`
	Answer         confirmation.Answer `json:"answer"`
}

type PermissionModeChangeResponse struct {
	Previous permission.Mode `json:"previous,omitempty"`
	Current  permission.Mode `json:"current"`
}

func newEventResponse(event agentruntime.AgentEvent) EventResponse {
	response := EventResponse{
		Sequence:  event.Sequence,
		SessionID: event.SessionID,
		TurnID:    event.TurnID,
		Type:      event.Type,
		Reason:    event.Reason,
	}
	if event.Error != nil {
		response.Error = event.Error.Error()
	}
	if event.Message != nil {
		value := newMessageResponse(*event.Message)
		response.Message = &value
	}
	if event.Type == agentruntime.ProviderEventReceived {
		value := ProviderEventResponse{
			Type:         event.ProviderEvent.Type,
			Content:      event.ProviderEvent.Content,
			Reasoning:    event.ProviderEvent.Reasoning,
			FinishReason: event.ProviderEvent.FinishReason,
			Payload:      newProviderEventPayloadResponse(event.ProviderEvent.Payload),
		}
		if event.ProviderEvent.Tool != nil {
			value.Tool = &ProviderToolEventResponse{
				Index: event.ProviderEvent.Tool.Index, ID: event.ProviderEvent.Tool.ID,
				Type: event.ProviderEvent.Tool.Type, Name: event.ProviderEvent.Tool.Name,
				Arguments: event.ProviderEvent.Tool.Arguments,
			}
		}
		if event.ProviderEvent.Error != nil {
			value.Error = event.ProviderEvent.Error.Error()
		}
		response.ProviderEvent = &value
	}
	if event.ToolRequest != nil {
		value := ToolRequestResponse{SessionID: event.ToolRequest.SessionID, TurnID: event.ToolRequest.TurnID, Call: newToolCallResponse(event.ToolRequest.Call)}
		response.ToolRequest = &value
	}
	if event.ToolResult != nil {
		value := ToolResultEnvelopeResponse{SessionID: event.ToolResult.SessionID, TurnID: event.ToolResult.TurnID, Result: newToolResultResponse(event.ToolResult.Result), TurnBehavior: event.ToolResult.TurnBehavior}
		response.ToolResult = &value
	}
	if event.Result != nil {
		value := newRunResultResponse(*event.Result)
		response.Result = &value
	}
	if event.Permission != nil {
		value := newPermissionRequestResponse(*event.Permission)
		response.Permission = &value
	}
	if event.Decision != nil {
		value := newDecisionResponse(*event.Decision)
		response.Decision = &value
	}
	if event.PermissionMode != nil {
		response.PermissionMode = &PermissionModeChangeResponse{Previous: event.PermissionMode.Previous, Current: event.PermissionMode.Current}
	}
	if event.Confirmation != nil {
		value := newConfirmationRequestResponse(*event.Confirmation)
		response.Confirmation = &value
	}
	if event.ConfirmationDecision != nil {
		value := newConfirmationDecisionResponse(*event.ConfirmationDecision)
		response.ConfirmationDecision = &value
	}
	return response
}

func newProviderEventPayloadResponse(payload any) *ProviderEventPayloadResponse {
	var result provider.StreamResult
	switch value := payload.(type) {
	case provider.StreamCompletedPayload:
		result = value.Result
	case *provider.StreamCompletedPayload:
		if value == nil {
			return nil
		}
		result = value.Result
	case provider.StreamFailedPayload:
		if value.Error == nil {
			return nil
		}
		return &ProviderEventPayloadResponse{Error: value.Error.Error()}
	case *provider.StreamFailedPayload:
		if value == nil || value.Error == nil {
			return nil
		}
		return &ProviderEventPayloadResponse{Error: value.Error.Error()}
	default:
		return nil
	}

	response := ProviderStreamResultResponse{
		Content:   result.Content,
		Reasoning: result.Reasoning,
		Finished:  result.Finished,
	}
	if result.CompletedTools != nil {
		response.CompletedTools = make([]ProviderCompletedToolCallResponse, len(result.CompletedTools))
		for index, tool := range result.CompletedTools {
			response.CompletedTools[index] = ProviderCompletedToolCallResponse{
				ID:        tool.ID,
				Type:      tool.Type,
				Name:      tool.Name,
				Arguments: tool.Arguments,
			}
		}
	}
	return &ProviderEventPayloadResponse{Result: &response}
}

func newMessageResponse(message agentruntime.Message) MessageResponse {
	response := MessageResponse{
		ID: message.ID, SessionID: message.SessionID, TurnID: message.TurnID,
		Type: message.Type, Content: message.Content, Reasoning: message.Reasoning, CreatedAt: message.CreatedAt,
	}
	if message.ToolCalls != nil {
		response.ToolCalls = make([]ToolCallResponse, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			response.ToolCalls[index] = newToolCallResponse(call)
		}
	}
	if message.ToolResult != nil {
		value := newToolResultResponse(*message.ToolResult)
		response.ToolResult = &value
	}
	return response
}

func newToolCallResponse(call agentruntime.ToolCall) ToolCallResponse {
	return ToolCallResponse{CallID: call.CallID, Name: call.Name, Arguments: call.Arguments}
}

func newToolResultResponse(result agentruntime.ToolResult) ToolResultResponse {
	return ToolResultResponse{
		CallID: result.CallID, Name: result.Name, Status: result.Status,
		Output: result.Output, Error: result.Error, TriggerSatisfied: result.TriggerSatisfied,
	}
}

func newRunResultResponse(result agentruntime.RunResult) RunResultResponse {
	response := RunResultResponse{
		SessionID: result.SessionID, TurnID: result.TurnID, Content: result.Content,
		Reasoning: result.Reasoning, Steps: result.Steps, Finished: result.Finished,
	}
	if result.ToolResults != nil {
		response.ToolResults = make([]ToolResultResponse, len(result.ToolResults))
		for index, toolResult := range result.ToolResults {
			response.ToolResults[index] = newToolResultResponse(toolResult)
		}
	}
	return response
}

func newPermissionRequestResponse(request permission.Request) PermissionRequestResponse {
	return PermissionRequestResponse{
		ID: request.ID, SessionID: request.SessionID, TurnID: request.TurnID,
		CallID: request.CallID, ToolName: request.ToolName, Details: request.Details,
		Reason: request.Reason, Risk: request.Risk, Actions: request.Actions,
		CreatedAt: request.CreatedAt, ExpiresAt: request.ExpiresAt,
	}
}

func newDecisionResponse(decision permission.Decision) DecisionResponse {
	return DecisionResponse{
		PermissionID: decision.PermissionID, SessionID: decision.SessionID,
		TurnID: decision.TurnID, CallID: decision.CallID, Type: decision.Type,
	}
}

func newConfirmationRequestResponse(request confirmation.Request) ConfirmationRequestResponse {
	return ConfirmationRequestResponse{ID: request.ID, SessionID: request.SessionID, TurnID: request.TurnID, CallID: request.CallID, ToolName: request.ToolName, Title: request.Title, Message: request.Message, Details: request.Details, CreatedAt: request.CreatedAt, ExpiresAt: request.ExpiresAt}
}

func newConfirmationDecisionResponse(decision confirmation.Decision) ConfirmationDecisionResponseValue {
	return ConfirmationDecisionResponseValue{ConfirmationID: decision.ConfirmationID, SessionID: decision.SessionID, TurnID: decision.TurnID, CallID: decision.CallID, Answer: decision.Answer}
}
