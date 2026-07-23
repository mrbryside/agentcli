package agentcli

import (
	"context"
	"sort"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
)

// SubagentConfirmationEventType identifies a child confirmation lifecycle
// fact delivered to the parent session.
type SubagentConfirmationEventType string

const (
	SubagentConfirmationRequested SubagentConfirmationEventType = "requested"
	SubagentConfirmationResolved  SubagentConfirmationEventType = "resolved"
	SubagentConfirmationCancelled SubagentConfirmationEventType = "cancelled"
	SubagentConfirmationExpired   SubagentConfirmationEventType = "expired"
)

// SubagentConfirmationEvent is a parent-addressed child confirmation fact.
// Request creation remains durable in ConfirmationStorage; this event is the
// immediate notification path and includes enough identity to route a reply.
type SubagentConfirmationEvent struct {
	Type            SubagentConfirmationEventType
	ParentSessionID string
	ParentTurnID    string
	SubagentID      string
	SubagentName    string
	DisplayName     string
	SessionID       string
	TurnID          string
	Request         *confirmation.Request
	Decision        *confirmation.Decision
}

type subagentConfirmationSubscriber struct {
	channel chan SubagentConfirmationEvent
	notify  chan struct{}
	queue   []SubagentConfirmationEvent
	closed  bool
}

func (m *subagentManager) subscribeConfirmations(ctx context.Context) <-chan SubagentConfirmationEvent {
	ctx = nonNilContext(ctx)
	subscriber := &subagentConfirmationSubscriber{
		channel: make(chan SubagentConfirmationEvent, 8),
		notify:  make(chan struct{}, 1),
	}
	var id uint64
	m.confirmationMu.Lock()
	if m.confirmationsClosed {
		subscriber.closed = true
	} else {
		m.nextConfirmationSubscriber++
		id = m.nextConfirmationSubscriber
		m.confirmationSubscribers[id] = subscriber
	}
	m.confirmationMu.Unlock()
	go m.deliverConfirmations(ctx, id, subscriber)
	return subscriber.channel
}

func (m *subagentManager) deliverConfirmations(ctx context.Context, id uint64, subscriber *subagentConfirmationSubscriber) {
	defer close(subscriber.channel)
	defer func() {
		if id == 0 {
			return
		}
		m.confirmationMu.Lock()
		delete(m.confirmationSubscribers, id)
		m.confirmationMu.Unlock()
	}()
	for {
		m.confirmationMu.Lock()
		if len(subscriber.queue) != 0 {
			event := cloneSubagentConfirmationEvent(subscriber.queue[0])
			subscriber.queue = subscriber.queue[1:]
			m.confirmationMu.Unlock()
			select {
			case subscriber.channel <- event:
			case <-ctx.Done():
				return
			}
			continue
		}
		closed := subscriber.closed
		m.confirmationMu.Unlock()
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

func (m *subagentManager) publishConfirmation(event SubagentConfirmationEvent) {
	m.confirmationMu.Lock()
	defer m.confirmationMu.Unlock()
	if m.confirmationsClosed {
		return
	}
	for _, subscriber := range m.confirmationSubscribers {
		subscriber.queue = append(subscriber.queue, cloneSubagentConfirmationEvent(event))
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func (m *subagentManager) closeConfirmations() {
	m.confirmationMu.Lock()
	defer m.confirmationMu.Unlock()
	if m.confirmationsClosed {
		return
	}
	m.confirmationsClosed = true
	for _, subscriber := range m.confirmationSubscribers {
		subscriber.closed = true
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func (m *subagentManager) subagentConfirmationEvent(id string, event agentruntime.AgentEvent) (SubagentConfirmationEvent, bool) {
	var eventType SubagentConfirmationEventType
	switch event.Type {
	case agentruntime.AgentConfirmationRequested:
		eventType = SubagentConfirmationRequested
	case agentruntime.AgentConfirmationResolved:
		eventType = SubagentConfirmationResolved
	case agentruntime.AgentConfirmationCancelled:
		eventType = SubagentConfirmationCancelled
	case agentruntime.AgentConfirmationExpired:
		eventType = SubagentConfirmationExpired
	default:
		return SubagentConfirmationEvent{}, false
	}
	record, found, err := m.store.Get(context.Background(), id)
	if err != nil || !found {
		return SubagentConfirmationEvent{}, false
	}
	result := SubagentConfirmationEvent{
		Type: eventType, ParentSessionID: record.ParentSessionID, ParentTurnID: record.ParentTurnID,
		SubagentID: record.ID, SubagentName: record.DefinitionName, DisplayName: record.DisplayName,
		SessionID: record.SessionID, TurnID: event.TurnID,
	}
	if event.Confirmation != nil {
		request := *event.Confirmation
		result.Request = &request
	}
	if event.ConfirmationDecision != nil {
		decision := *event.ConfirmationDecision
		result.Decision = &decision
	}
	return result, true
}

func (m *subagentManager) pendingConfirmations(ctx context.Context, parentSessionID string) ([]SubagentConfirmationEvent, error) {
	records, err := m.List(ctx, parentSessionID, true)
	if err != nil {
		return nil, err
	}
	events := make([]SubagentConfirmationEvent, 0)
	for _, record := range records {
		for _, pending := range m.config.confirmations.Pending(record.SessionID) {
			request := pending.Request
			events = append(events, SubagentConfirmationEvent{
				Type: SubagentConfirmationRequested, ParentSessionID: record.ParentSessionID, ParentTurnID: record.ParentTurnID,
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

func cloneSubagentConfirmationEvent(event SubagentConfirmationEvent) SubagentConfirmationEvent {
	clone := event
	if event.Request != nil {
		request := *event.Request
		if event.Request.ExpiresAt != nil {
			expiry := *event.Request.ExpiresAt
			request.ExpiresAt = &expiry
		}
		clone.Request = &request
	}
	if event.Decision != nil {
		decision := *event.Decision
		clone.Decision = &decision
	}
	return clone
}
