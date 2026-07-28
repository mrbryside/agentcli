package agentcli

import (
	"context"

	"github.com/mrbryside/agentcli/storage"
)

// SystemEventType identifies a live agent-level fact that is not owned by one
// runtime turn.
type SystemEventType string

const (
	// SystemSubagentClosed reports one successful explicit or automatic subagent
	// close.
	SystemSubagentClosed SystemEventType = "subagent_closed"
	// SystemTaskCompleted reports one terminal task result and any validated
	// application-only metadata from its final-result contract.
	SystemTaskCompleted SystemEventType = "task_completed"
)

// SystemEvent is one main-agent-session system fact. Payload fields are selected
// by Type so callers can use a stable subscription as new system facts are
// added.
type SystemEvent struct {
	Type               SystemEventType
	MainAgentSessionID string
	MainAgentTurnID    string
	SubagentClosed     *SubagentClosedEvent
	TaskCompleted      *TaskCompletedEvent
}

// TaskCompletedEvent describes one completed, incomplete, or errored task.
// Metadata is application-only and is never added to provider context.
type TaskCompletedEvent struct {
	TaskID            string
	SubagentSessionID string
	SubagentTurnID    string
	AgentName         string
	State             TaskState
	Metadata          map[string]any
}

// SubagentClosedEvent describes the subagent state removed by one successful
// explicit or automatic close.
type SubagentClosedEvent struct {
	Subagent             storage.Subagent
	PreviousStatus       storage.SubagentStatus
	PreviousResultStatus storage.SubagentResultStatus
	DroppedMessages      int
	Interrupted          bool
	Automatic            bool
}

type systemEventSubscriber struct {
	channel chan SystemEvent
	notify  chan struct{}
	queue   []SystemEvent
	closed  bool
}

func (m *subagentManager) subscribeSystemEvents(ctx context.Context) <-chan SystemEvent {
	ctx = nonNilContext(ctx)
	subscriber := &systemEventSubscriber{
		channel: make(chan SystemEvent, 8),
		notify:  make(chan struct{}, 1),
	}
	var id uint64
	m.systemEventMu.Lock()
	if m.systemEventsClosed {
		subscriber.closed = true
	} else {
		m.nextSystemEventSubscriber++
		id = m.nextSystemEventSubscriber
		m.systemEventSubscribers[id] = subscriber
	}
	m.systemEventMu.Unlock()
	go m.deliverSystemEvents(ctx, id, subscriber)
	return subscriber.channel
}

func (m *subagentManager) deliverSystemEvents(ctx context.Context, id uint64, subscriber *systemEventSubscriber) {
	defer close(subscriber.channel)
	defer func() {
		if id == 0 {
			return
		}
		m.systemEventMu.Lock()
		delete(m.systemEventSubscribers, id)
		m.systemEventMu.Unlock()
	}()
	for {
		m.systemEventMu.Lock()
		if len(subscriber.queue) != 0 {
			event := cloneSystemEvent(subscriber.queue[0])
			subscriber.queue = subscriber.queue[1:]
			m.systemEventMu.Unlock()
			select {
			case subscriber.channel <- event:
			case <-ctx.Done():
				return
			}
			continue
		}
		closed := subscriber.closed
		m.systemEventMu.Unlock()
		if closed {
			return
		}
		select {
		case <-subscriber.notify:
		case <-ctx.Done():
			return
		}
	}
}

func (m *subagentManager) publishSystemEvent(event SystemEvent) {
	if m != nil && m.config.logger != nil {
		switch event.Type {
		case SystemSubagentClosed:
			if event.SubagentClosed == nil {
				m.config.logger.Warn("agent system event missing payload",
					"event_type", event.Type,
					"main_agent_session_id", event.MainAgentSessionID,
					"main_agent_turn_id", event.MainAgentTurnID,
				)
				break
			}
			closed := event.SubagentClosed
			subagent := closed.Subagent
			m.config.logger.Info("subagent closed",
				"event_type", event.Type,
				"main_agent_session_id", event.MainAgentSessionID,
				"main_agent_turn_id", event.MainAgentTurnID,
				"subagent_id", subagent.ID,
				"automatic", closed.Automatic,
			)
			m.config.logger.Debug("subagent closed details",
				"event_type", event.Type,
				"main_agent_session_id", event.MainAgentSessionID,
				"main_agent_turn_id", event.MainAgentTurnID,
				"subagent_id", subagent.ID,
				"subagent_session_id", subagent.SubagentSessionID,
				"subagent_name", subagent.DisplayName,
				"subagent_definition", subagent.DefinitionName,
				"previous_status", closed.PreviousStatus,
				"previous_outcome", closed.PreviousResultStatus,
				"final_status", subagent.Status,
				"final_outcome", subagent.LastResultStatus,
				"dropped_messages", closed.DroppedMessages,
				"interrupted", closed.Interrupted,
				"automatic", closed.Automatic,
			)
		case SystemTaskCompleted:
			if event.TaskCompleted == nil {
				m.config.logger.Warn("agent system event missing payload",
					"event_type", event.Type,
					"main_agent_session_id", event.MainAgentSessionID,
					"main_agent_turn_id", event.MainAgentTurnID,
				)
				break
			}
			completed := event.TaskCompleted
			m.config.logger.Info("task completed",
				"event_type", event.Type,
				"main_agent_session_id", event.MainAgentSessionID,
				"main_agent_turn_id", event.MainAgentTurnID,
				"task_id", completed.TaskID,
				"subagent_session_id", completed.SubagentSessionID,
				"subagent_turn_id", completed.SubagentTurnID,
				"agent_name", completed.AgentName,
				"state", completed.State,
			)
		default:
			m.config.logger.Debug("agent system event",
				"event_type", event.Type,
				"main_agent_session_id", event.MainAgentSessionID,
				"main_agent_turn_id", event.MainAgentTurnID,
			)
		}
	}
	m.systemEventMu.Lock()
	defer m.systemEventMu.Unlock()
	if m.systemEventsClosed {
		return
	}
	for _, subscriber := range m.systemEventSubscribers {
		subscriber.queue = append(subscriber.queue, cloneSystemEvent(event))
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func (m *subagentManager) closeSystemEvents() {
	m.systemEventMu.Lock()
	defer m.systemEventMu.Unlock()
	if m.systemEventsClosed {
		return
	}
	m.systemEventsClosed = true
	for _, subscriber := range m.systemEventSubscribers {
		subscriber.closed = true
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func cloneSystemEvent(event SystemEvent) SystemEvent {
	clone := event
	if event.SubagentClosed != nil {
		subagentClosed := *event.SubagentClosed
		subagentClosed.Subagent = storage.CloneSubagent(event.SubagentClosed.Subagent)
		clone.SubagentClosed = &subagentClosed
	}
	if event.TaskCompleted != nil {
		taskCompleted := *event.TaskCompleted
		taskCompleted.Metadata = cloneTaskMetadata(event.TaskCompleted.Metadata)
		clone.TaskCompleted = &taskCompleted
	}
	return clone
}
