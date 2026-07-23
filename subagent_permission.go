package agentcli

import (
	"context"
	"sort"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
)

// SubagentPermissionEventType identifies a child permission lifecycle fact
// delivered to the parent session.
type SubagentPermissionEventType string

const (
	SubagentPermissionRequested SubagentPermissionEventType = "requested"
	SubagentPermissionResolved  SubagentPermissionEventType = "resolved"
	SubagentPermissionCancelled SubagentPermissionEventType = "cancelled"
	SubagentPermissionExpired   SubagentPermissionEventType = "expired"
)

// SubagentPermissionEvent is a parent-addressed child permission fact.
// Request creation remains durable in PermissionStorage; this event is the
// immediate notification path and includes enough identity to route a reply.
type SubagentPermissionEvent struct {
	Type            SubagentPermissionEventType
	ParentSessionID string
	ParentTurnID    string
	SubagentID      string
	SubagentName    string
	DisplayName     string
	SessionID       string
	TurnID          string
	Request         *permission.Request
	Decision        *permission.Decision
}

type subagentPermissionSubscriber struct {
	channel chan SubagentPermissionEvent
	notify  chan struct{}
	queue   []SubagentPermissionEvent
	closed  bool
}

func (m *subagentManager) subscribePermissions(ctx context.Context) <-chan SubagentPermissionEvent {
	ctx = nonNilContext(ctx)
	subscriber := &subagentPermissionSubscriber{
		channel: make(chan SubagentPermissionEvent, 8),
		notify:  make(chan struct{}, 1),
	}
	var id uint64
	m.permissionMu.Lock()
	if m.permissionsClosed {
		subscriber.closed = true
	} else {
		m.nextPermissionSubscriber++
		id = m.nextPermissionSubscriber
		m.permissionSubscribers[id] = subscriber
	}
	m.permissionMu.Unlock()
	go m.deliverPermissions(ctx, id, subscriber)
	return subscriber.channel
}

func (m *subagentManager) deliverPermissions(ctx context.Context, id uint64, subscriber *subagentPermissionSubscriber) {
	defer close(subscriber.channel)
	defer func() {
		if id == 0 {
			return
		}
		m.permissionMu.Lock()
		delete(m.permissionSubscribers, id)
		m.permissionMu.Unlock()
	}()
	for {
		m.permissionMu.Lock()
		if len(subscriber.queue) != 0 {
			event := cloneSubagentPermissionEvent(subscriber.queue[0])
			subscriber.queue = subscriber.queue[1:]
			m.permissionMu.Unlock()
			select {
			case subscriber.channel <- event:
			case <-ctx.Done():
				return
			}
			continue
		}
		closed := subscriber.closed
		m.permissionMu.Unlock()
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

func (m *subagentManager) publishPermission(event SubagentPermissionEvent) {
	m.permissionMu.Lock()
	defer m.permissionMu.Unlock()
	if m.permissionsClosed {
		return
	}
	for _, subscriber := range m.permissionSubscribers {
		subscriber.queue = append(subscriber.queue, cloneSubagentPermissionEvent(event))
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func (m *subagentManager) closePermissions() {
	m.permissionMu.Lock()
	defer m.permissionMu.Unlock()
	if m.permissionsClosed {
		return
	}
	m.permissionsClosed = true
	for _, subscriber := range m.permissionSubscribers {
		subscriber.closed = true
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func (m *subagentManager) subagentPermissionEvent(id string, event agentruntime.AgentEvent) (SubagentPermissionEvent, bool) {
	var eventType SubagentPermissionEventType
	switch event.Type {
	case agentruntime.AgentPermissionRequested:
		eventType = SubagentPermissionRequested
	case agentruntime.AgentPermissionResolved:
		eventType = SubagentPermissionResolved
	case agentruntime.AgentPermissionCancelled:
		eventType = SubagentPermissionCancelled
	case agentruntime.AgentPermissionExpired:
		eventType = SubagentPermissionExpired
	default:
		return SubagentPermissionEvent{}, false
	}
	record, found, err := m.store.Get(context.Background(), id)
	if err != nil || !found {
		return SubagentPermissionEvent{}, false
	}
	result := SubagentPermissionEvent{
		Type: eventType, ParentSessionID: record.ParentSessionID, ParentTurnID: record.ParentTurnID,
		SubagentID: record.ID, SubagentName: record.DefinitionName, DisplayName: record.DisplayName,
		SessionID: record.SessionID, TurnID: event.TurnID,
	}
	if event.Permission != nil {
		request := cloneSubagentPermissionRequest(*event.Permission)
		result.Request = &request
	}
	if event.Decision != nil {
		decision := *event.Decision
		result.Decision = &decision
	}
	return result, true
}

func (m *subagentManager) pendingPermissions(ctx context.Context, parentSessionID string) ([]SubagentPermissionEvent, error) {
	records, err := m.List(ctx, parentSessionID, true)
	if err != nil {
		return nil, err
	}
	events := make([]SubagentPermissionEvent, 0)
	for _, record := range records {
		for _, pending := range m.config.permissions.Pending(record.SessionID) {
			request := cloneSubagentPermissionRequest(pending.Request)
			events = append(events, SubagentPermissionEvent{
				Type: SubagentPermissionRequested, ParentSessionID: record.ParentSessionID, ParentTurnID: record.ParentTurnID,
				SubagentID: record.ID, SubagentName: record.DefinitionName, DisplayName: record.DisplayName,
				SessionID: record.SessionID, TurnID: request.TurnID, Request: &request,
			})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		left, right := events[i].Request, events[j].Request
		if left.CreatedAt.Equal(right.CreatedAt) {
			return left.ID < right.ID
		}
		return left.CreatedAt.Before(right.CreatedAt)
	})
	return events, nil
}

func cloneSubagentPermissionEvent(event SubagentPermissionEvent) SubagentPermissionEvent {
	clone := event
	if event.Request != nil {
		request := cloneSubagentPermissionRequest(*event.Request)
		clone.Request = &request
	}
	if event.Decision != nil {
		decision := *event.Decision
		clone.Decision = &decision
	}
	return clone
}

func cloneSubagentPermissionRequest(request permission.Request) permission.Request {
	clone := request
	clone.Actions = append([]permission.Action(nil), request.Actions...)
	if request.ExpiresAt != nil {
		expiry := *request.ExpiresAt
		clone.ExpiresAt = &expiry
	}
	return clone
}
