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

// ServerTurnSource explains why the HTTP server created a root turn.
type ServerTurnSource string

const (
	ServerTurnSourceUser             ServerTurnSource = "user"
	ServerTurnSourceSubagentCallback ServerTurnSource = "subagent_callback"
)

// SessionActivityType identifies server lifecycle records around ordinary
// runtime events. Runtime events use SessionActivityTurnEvent and retain their
// original type in RuntimeEvent.Type.
type SessionActivityType string

const (
	SessionActivityTurnQueued    SessionActivityType = "turn_queued"
	SessionActivityTurnAdmitted  SessionActivityType = "turn_admitted"
	SessionActivityTurnCancelled SessionActivityType = "turn_cancelled"
	SessionActivityTurnRejected  SessionActivityType = "turn_rejected"
	SessionActivityTurnEvent     SessionActivityType = "turn_event"
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

// SubagentCallbackReference identifies the child completion that caused an
// automatic parent continuation without duplicating its answer.
type SubagentCallbackReference struct {
	SubagentID     string                 `json:"subagent_id"`
	DisplayName    string                 `json:"display_name,omitempty"`
	DefinitionName string                 `json:"definition_name"`
	ChildSessionID string                 `json:"child_session_id"`
	ChildTurnID    string                 `json:"child_turn_id"`
	Status         SubagentCallbackStatus `json:"status"`
	Summary        string                 `json:"summary,omitempty"`
	NextStep       string                 `json:"next_step,omitempty"`
}

// SessionEventResponse is the session-wide SSE envelope. Cursor is monotonic
// across every root turn in one session and is independent from the
// per-turn RuntimeEvent.Sequence cursor.
type SessionEventResponse struct {
	Cursor           uint64                     `json:"cursor"`
	Type             SessionActivityType        `json:"type"`
	Source           ServerTurnSource           `json:"source"`
	SessionID        string                     `json:"session_id"`
	TurnID           string                     `json:"turn_id"`
	QueuePosition    int                        `json:"queue_position,omitempty"`
	TurnURL          string                     `json:"turn_url"`
	EventsURL        string                     `json:"events_url"`
	Error            string                     `json:"error,omitempty"`
	SubagentCallback *SubagentCallbackReference `json:"subagent_callback,omitempty"`
	RuntimeEvent     *EventResponse             `json:"runtime_event,omitempty"`
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

// SubagentDefinitionResponse is the safe, discovery-only HTTP view of a
// project definition. Instructions and local paths are intentionally omitted.
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

// CreateSubagentRequest starts a child session from a project definition.
// ParentTurnID is optional for direct UI creation; the server assigns a
// synthetic ID when it is not supplied.
type CreateSubagentRequest struct {
	Name         string `json:"name" validate:"required"`
	Message      string `json:"message" validate:"required"`
	Label        string `json:"label,omitempty"`
	ParentTurnID string `json:"parent_turn_id,omitempty"`
}

type SendSubagentMessageRequest struct {
	Message string `json:"message" validate:"required"`
}

// SubagentResponse is an HTTP-safe summary of one child instance. Pending
// message content remains private to the manager mailbox.
type SubagentResponse struct {
	ID               string                      `json:"id"`
	DisplayName      string                      `json:"display_name"`
	Label            string                      `json:"label,omitempty"`
	ParentSessionID  string                      `json:"parent_session_id"`
	ParentTurnID     string                      `json:"parent_turn_id"`
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
	CreatedAt        time.Time                   `json:"created_at"`
	UpdatedAt        time.Time                   `json:"updated_at"`
	ClosedAt         *time.Time                  `json:"closed_at,omitempty"`
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
	ID         string                   `json:"id"`
	SessionID  string                   `json:"session_id"`
	TurnID     string                   `json:"turn_id"`
	Type       agentruntime.MessageType `json:"type"`
	Content    string                   `json:"content,omitempty"`
	Reasoning  string                   `json:"reasoning,omitempty"`
	ToolCalls  []ToolCallResponse       `json:"tool_calls,omitempty"`
	ToolResult *ToolResultResponse      `json:"tool_result,omitempty"`
	CreatedAt  time.Time                `json:"created_at"`
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
	Type         provider.EventType         `json:"type"`
	Content      string                     `json:"content,omitempty"`
	Reasoning    string                     `json:"reasoning,omitempty"`
	Tool         *ProviderToolEventResponse `json:"tool,omitempty"`
	Error        string                     `json:"error,omitempty"`
	FinishReason string                     `json:"finish_reason,omitempty"`
}

type ProviderToolEventResponse struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
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
	return ToolResultResponse{CallID: result.CallID, Name: result.Name, Status: result.Status, Output: result.Output, Error: result.Error}
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
