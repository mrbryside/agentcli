package agentcli

func (server *Server) forwardScopeEvents(events <-chan ScopeEvent) {
	for {
		select {
		case <-server.context.Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			if !server.waitForTriggerTurnEvents(event) {
				return
			}
			server.sessionEvents.publish(newSessionScopeEvent(event))
		}
	}
}

func (server *Server) waitForTriggerTurnEvents(event ScopeEvent) bool {
	key := serverRunKey{sessionID: event.SessionID, turnID: event.TriggerTurnID}
	server.runsMu.RLock()
	turn := server.turns[key]
	server.runsMu.RUnlock()
	if turn == nil {
		return true
	}
	select {
	case <-turn.runtimeEventsDone:
		return true
	case <-server.context.Done():
		return false
	}
}

func newSessionScopeEvent(event ScopeEvent) SessionEventResponse {
	return SessionEventResponse{
		Type:      SessionActivityScopeEvent,
		Source:    ServerTurnSourceUser,
		SessionID: event.SessionID,
		TurnID:    event.TriggerTurnID,
		ScopeEvent: &ScopeEventResponse{
			Type:          event.Type,
			SessionID:     event.SessionID,
			ScopeID:       event.ScopeID,
			TriggerTurnID: event.TriggerTurnID,
			ChildIDs:      append([]string{}, event.ChildIDs...),
			ToolNames:     append([]string{}, event.ToolNames...),
			OccurredAt:    event.OccurredAt,
		},
	}
}
