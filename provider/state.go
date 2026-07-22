package provider

// StreamState is an immutable snapshot of the stream event history.
type StreamState struct {
	events []StreamEvent
}

// EmptyState creates a state with no events.
func EmptyState() StreamState {
	return StreamState{}
}

// State returns a new state with event appended. Neither the input state nor
// the supplied event is mutated or retained by reference.
func State(current StreamState, event StreamEvent) StreamState {
	nextEvents := make([]StreamEvent, len(current.events)+1)
	for i, existing := range current.events {
		nextEvents[i] = cloneEvent(existing)
	}
	nextEvents[len(current.events)] = cloneEvent(event)
	return StreamState{events: nextEvents}
}

// Events returns a defensive copy of the complete event history.
func Events(state StreamState) []StreamEvent {
	history := make([]StreamEvent, len(state.events))
	for i, event := range state.events {
		history[i] = cloneEvent(event)
	}
	return history
}

func cloneEvent(event StreamEvent) StreamEvent {
	clone := event
	if event.Tool != nil {
		tool := *event.Tool
		clone.Tool = &tool
	}
	clone.Payload = clonePayload(event.Payload)
	return clone
}

func clonePayload(payload any) any {
	switch value := payload.(type) {
	case StreamCompletedPayload:
		value.Result = cloneResult(value.Result)
		return value
	case *StreamCompletedPayload:
		if value == nil {
			return (*StreamCompletedPayload)(nil)
		}
		clone := *value
		clone.Result = cloneResult(value.Result)
		return &clone
	case StreamFailedPayload:
		return value
	case *StreamFailedPayload:
		if value == nil {
			return (*StreamFailedPayload)(nil)
		}
		clone := *value
		return &clone
	default:
		return payload
	}
}

func cloneResult(result StreamResult) StreamResult {
	clone := result
	clone.CompletedTools = make([]ToolCall, len(result.CompletedTools))
	for i, tool := range result.CompletedTools {
		clone.CompletedTools[i] = cloneToolCall(tool)
	}
	return clone
}

func cloneToolCall(tool ToolCall) ToolCall {
	clone := tool
	clone.Arguments = make(map[string]any, len(tool.Arguments))
	for key, value := range tool.Arguments {
		clone.Arguments[key] = value
	}
	return clone
}
