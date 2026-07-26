package agentcli

func (server *Server) forwardSystemEvents(events <-chan SystemEvent) {
	for {
		select {
		case <-server.context.Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			switch event.Type {
			case SystemSubagentClosed:
				if event.SubagentClosed == nil {
					continue
				}
				server.sessionEvents.publish(SessionEventResponse{
					Type:           SessionActivitySubagentClosed,
					Source:         ServerTurnSourceSubagentLifecycle,
					SessionID:      event.SessionID,
					TurnID:         event.TurnID,
					SubagentClosed: newSubagentClosedReference(*event.SubagentClosed),
				})
			}
		}
	}
}

func newSubagentClosedReference(event SubagentClosedEvent) *SubagentClosedReference {
	return &SubagentClosedReference{
		Subagent:        newSubagentResponse(event.Subagent),
		PreviousStatus:  event.PreviousStatus,
		PreviousOutcome: event.PreviousOutcome,
		DroppedMessages: event.DroppedMessages,
		Interrupted:     event.Interrupted,
		Automatic:       event.Automatic,
	}
}
