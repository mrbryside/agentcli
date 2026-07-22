package agentruntime

// AgentState is an immutable snapshot of an append-only AgentEvent history.
type AgentState struct {
	events []AgentEvent
}

// EmptyState creates a state with no events.
func EmptyState() AgentState {
	return AgentState{}
}

// State returns a new state with event appended. Neither the prior state nor
// the supplied event is retained by reference.
func State(current AgentState, event AgentEvent) AgentState {
	nextEvents := make([]AgentEvent, len(current.events)+1)
	for index, existing := range current.events {
		nextEvents[index] = cloneEvent(existing)
	}
	nextEvents[len(current.events)] = cloneEvent(event)
	return AgentState{events: nextEvents}
}

// Events returns a defensive copy of the complete event history.
func Events(state AgentState) []AgentEvent {
	history := make([]AgentEvent, len(state.events))
	for index, event := range state.events {
		history[index] = cloneEvent(event)
	}
	return history
}
