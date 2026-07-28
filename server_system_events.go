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
			case SystemTaskCompleted:
				if event.TaskCompleted == nil {
					continue
				}
				server.sessionEvents.publish(SessionEventResponse{
					Type:          SessionActivityTaskCompleted,
					Source:        ServerTurnSourceTask,
					SessionID:     event.MainAgentSessionID,
					TurnID:        event.MainAgentTurnID,
					TaskCompleted: newTaskCompletedReference(*event.TaskCompleted),
				})
			case SystemSubagentClosed:
				if event.SubagentClosed == nil {
					continue
				}
				server.sessionEvents.publish(SessionEventResponse{
					Type:           SessionActivitySubagentClosed,
					Source:         ServerTurnSourceSubagentLifecycle,
					SessionID:      event.MainAgentSessionID,
					TurnID:         event.MainAgentTurnID,
					SubagentClosed: newSubagentClosedReference(*event.SubagentClosed),
				})
			}
		}
	}
}

func newTaskCompletedReference(event TaskCompletedEvent) *TaskCompletedReference {
	return &TaskCompletedReference{
		TaskID:            event.TaskID,
		SubagentSessionID: event.SubagentSessionID,
		SubagentTurnID:    event.SubagentTurnID,
		AgentName:         event.AgentName,
		State:             event.State,
		Metadata:          cloneTaskMetadata(event.Metadata),
	}
}

func newSubagentClosedReference(event SubagentClosedEvent) *SubagentClosedReference {
	return &SubagentClosedReference{
		Subagent:             newSubagentResponse(event.Subagent),
		PreviousStatus:       event.PreviousStatus,
		PreviousResultStatus: event.PreviousResultStatus,
		DroppedMessages:      event.DroppedMessages,
		Interrupted:          event.Interrupted,
	}
}
