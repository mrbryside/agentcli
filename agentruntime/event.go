package agentruntime

import (
	"encoding/json"

	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
)

// EventType identifies a fact in an agent turn's event history.
type EventType string

const (
	RunStarted                 EventType = "run_started"
	ProviderEventReceived      EventType = "provider_event_received"
	ToolCallRequested          EventType = "tool_call_requested"
	ToolResultReceived         EventType = "tool_result_received"
	RunCompleted               EventType = "run_completed"
	RunFailed                  EventType = "run_failed"
	AgentInterrupted           EventType = "agent_interrupted"
	AgentPermissionRequested   EventType = "permission_requested"
	AgentPermissionResolved    EventType = "permission_resolved"
	AgentPermissionCancelled   EventType = "permission_cancelled"
	AgentPermissionExpired     EventType = "permission_expired"
	AgentConfirmationRequested EventType = "confirmation_requested"
	AgentConfirmationResolved  EventType = "confirmation_resolved"
	AgentConfirmationCancelled EventType = "confirmation_cancelled"
	AgentConfirmationExpired   EventType = "confirmation_expired"
	PermissionModeChanged      EventType = "permission_mode_changed"
)

// PermissionModeChange describes the permission mode in force for an event.
// RunStarted carries Current with an empty Previous. PermissionModeChanged
// always carries both values.
type PermissionModeChange struct {
	Previous permission.Mode
	Current  permission.Mode
}

// AgentEvent is one immutable fact observed while processing an agent turn.
type AgentEvent struct {
	Sequence             uint64
	SessionID            string
	TurnID               string
	Type                 EventType
	Message              *Message
	ProviderEvent        provider.StreamEvent
	ToolRequest          *ToolRequest
	ToolResult           *ToolResultEnvelope
	Result               *RunResult
	Error                error
	Reason               string
	Permission           *permission.Request
	Decision             *permission.Decision
	Confirmation         *confirmation.Request
	ConfirmationDecision *confirmation.Decision
	PermissionMode       *PermissionModeChange
}

// EventCursor identifies a position in one run's immutable event history.
// Sequence zero denotes the position before that run's first event.
type EventCursor struct {
	SessionID string
	TurnID    string
	Sequence  uint64
}

// EventSubscription is a live event stream fenced by Cursor. Events only
// contains events committed after Cursor; callers can recover the preceding
// retained range with Run.EventsBetween.
type EventSubscription struct {
	Cursor EventCursor
	Events <-chan AgentEvent
}

// Cursor returns the history position occupied by this event.
func (e AgentEvent) Cursor() EventCursor {
	return EventCursor{SessionID: e.SessionID, TurnID: e.TurnID, Sequence: e.Sequence}
}

func cloneEvent(event AgentEvent) AgentEvent {
	clone := event
	if event.Message != nil {
		message := storage.CloneMessage(*event.Message)
		clone.Message = &message
	}
	clone.ProviderEvent = cloneProviderEvent(event.ProviderEvent)
	if event.ToolRequest != nil {
		request := cloneToolRequest(*event.ToolRequest)
		clone.ToolRequest = &request
	}
	if event.ToolResult != nil {
		result := cloneToolResultEnvelope(*event.ToolResult)
		clone.ToolResult = &result
	}
	if event.Result != nil {
		result := cloneRunResult(*event.Result)
		clone.Result = &result
	}
	if event.Permission != nil {
		value := *event.Permission
		value.Actions = append([]permission.Action(nil), value.Actions...)
		if value.ExpiresAt != nil {
			expiry := *value.ExpiresAt
			value.ExpiresAt = &expiry
		}
		clone.Permission = &value
	}
	if event.Decision != nil {
		value := *event.Decision
		clone.Decision = &value
	}
	if event.Confirmation != nil {
		value := *event.Confirmation
		if value.ExpiresAt != nil {
			expiresAt := *value.ExpiresAt
			value.ExpiresAt = &expiresAt
		}
		clone.Confirmation = &value
	}
	if event.ConfirmationDecision != nil {
		value := *event.ConfirmationDecision
		clone.ConfirmationDecision = &value
	}
	if event.PermissionMode != nil {
		value := *event.PermissionMode
		clone.PermissionMode = &value
	}
	return clone
}

func cloneProviderEvent(event provider.StreamEvent) provider.StreamEvent {
	clone := event
	if event.Tool != nil {
		tool := *event.Tool
		clone.Tool = &tool
	}
	clone.Payload = cloneProviderPayload(event.Payload)
	return clone
}

// cloneProviderPayload intentionally handles only the provider package's
// documented terminal payloads plus raw JSON payloads. Other payload values
// are treated as immutable provider-owned values.
func cloneProviderPayload(payload any) any {
	switch value := payload.(type) {
	case provider.StreamCompletedPayload:
		value.Result = cloneProviderResult(value.Result)
		return value
	case *provider.StreamCompletedPayload:
		if value == nil {
			return (*provider.StreamCompletedPayload)(nil)
		}
		clone := *value
		clone.Result = cloneProviderResult(value.Result)
		return &clone
	case provider.StreamFailedPayload:
		return value
	case *provider.StreamFailedPayload:
		if value == nil {
			return (*provider.StreamFailedPayload)(nil)
		}
		clone := *value
		return &clone
	case json.RawMessage:
		return cloneRawJSON(value)
	case []byte:
		clone := make([]byte, len(value))
		copy(clone, value)
		return clone
	case map[string]any:
		return cloneProviderArguments(value)
	case []any:
		return cloneProviderValue(value)
	default:
		return payload
	}
}

func cloneProviderResult(result provider.StreamResult) provider.StreamResult {
	clone := result
	if result.CompletedTools != nil {
		clone.CompletedTools = make([]provider.ToolCall, len(result.CompletedTools))
		for index, tool := range result.CompletedTools {
			clone.CompletedTools[index] = tool
			clone.CompletedTools[index].Arguments = cloneProviderArguments(tool.Arguments)
		}
	}
	return clone
}

func cloneProviderArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return nil
	}
	clone := make(map[string]any, len(arguments))
	for key, value := range arguments {
		clone[key] = cloneProviderValue(value)
	}
	return clone
}

func cloneProviderValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneProviderArguments(value)
	case []any:
		clone := make([]any, len(value))
		for index, item := range value {
			clone[index] = cloneProviderValue(item)
		}
		return clone
	case json.RawMessage:
		return cloneRawJSON(value)
	case []byte:
		clone := make([]byte, len(value))
		copy(clone, value)
		return clone
	default:
		return value
	}
}
