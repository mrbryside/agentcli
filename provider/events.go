package provider

// EventType identifies a fact observed while a provider stream is running.
type EventType string

const (
	ContentReceived       EventType = "content_received"
	ReasoningReceived     EventType = "reasoning_received"
	ToolCallStarted       EventType = "tool_call_started"
	ToolArgumentsReceived EventType = "tool_arguments_received"
	ToolCallCompleted     EventType = "tool_call_completed"
	StreamCompleted       EventType = "stream_completed"
	StreamFailed          EventType = "stream_failed"
)

// ToolEvent carries a tool call or an incremental tool-call argument fragment.
type ToolEvent struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

// StreamEvent is the provider-neutral representation of a stream fact.
type StreamEvent struct {
	Type         EventType
	Content      string
	Reasoning    string
	Tool         *ToolEvent
	Error        error
	FinishReason string
	Payload      any
}

// StreamCompletedPayload is attached to the final completion event.
type StreamCompletedPayload struct {
	Result StreamResult
}

// StreamFailedPayload carries a terminal stream error.
type StreamFailedPayload struct {
	Error error
}

// ToolCall is a completed provider-neutral tool invocation.
type ToolCall struct {
	ID        string
	Type      string
	Name      string
	Arguments map[string]any
}

// StreamResult is the aggregate derived from the stream event history.
type StreamResult struct {
	Content        string
	Reasoning      string
	CompletedTools []ToolCall
	Finished       bool
}
