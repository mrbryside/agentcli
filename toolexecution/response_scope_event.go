package toolexecution

import (
	"context"
	"sync"
	"time"
)

// ScopeEventType identifies one boundary in response-scope shutdown.
type ScopeEventType string

const (
	// PreEndScope is emitted after the scope becomes quiescent and
	// before cleanup or any final EndResponseScope tool handler runs.
	PreEndScope ScopeEventType = "pre_end_scope"
	// EndScope is emitted after cleanup and all final EndResponseScope tool
	// handlers have run and the scope has been removed.
	EndScope ScopeEventType = "end_scope"
)

// ScopeEvent is a live-only lifecycle fact for one user response.
// ScopeID is the root human turn ID. TriggerTurnID is the turn whose
// completion made the scope quiescent.
type ScopeEvent struct {
	Type          ScopeEventType
	SessionID     string
	ScopeID       string
	TriggerTurnID string
	ChildIDs      []string
	ToolNames     []string
	OccurredAt    time.Time
}

type scopeEventSubscriber struct {
	channel chan ScopeEvent
	notify  chan struct{}
	queue   []ScopeEvent
	closed  bool
}

type scopeEventHub struct {
	ctx context.Context

	mu             sync.Mutex
	nextSubscriber uint64
	subscribers    map[uint64]*scopeEventSubscriber
	closed         bool
}

func newScopeEventHub(ctx context.Context) *scopeEventHub {
	if ctx == nil {
		ctx = context.Background()
	}
	hub := &scopeEventHub{
		ctx:         ctx,
		subscribers: make(map[uint64]*scopeEventSubscriber),
	}
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			hub.close()
		}()
	}
	return hub
}

func (hub *scopeEventHub) subscribe(ctx context.Context) <-chan ScopeEvent {
	if ctx == nil {
		ctx = context.Background()
	}
	subscriber := &scopeEventSubscriber{
		channel: make(chan ScopeEvent, 8),
		notify:  make(chan struct{}, 1),
	}
	var id uint64
	hub.mu.Lock()
	if hub.closed {
		subscriber.closed = true
	} else {
		hub.nextSubscriber++
		id = hub.nextSubscriber
		hub.subscribers[id] = subscriber
	}
	hub.mu.Unlock()
	go hub.deliver(ctx, id, subscriber)
	return subscriber.channel
}

func (hub *scopeEventHub) deliver(ctx context.Context, id uint64, subscriber *scopeEventSubscriber) {
	defer close(subscriber.channel)
	defer func() {
		if id == 0 {
			return
		}
		hub.mu.Lock()
		delete(hub.subscribers, id)
		hub.mu.Unlock()
	}()
	for {
		hub.mu.Lock()
		if len(subscriber.queue) != 0 {
			event := cloneScopeEvent(subscriber.queue[0])
			subscriber.queue = subscriber.queue[1:]
			hub.mu.Unlock()
			select {
			case subscriber.channel <- event:
			case <-ctx.Done():
				return
			case <-hub.ctx.Done():
				return
			}
			continue
		}
		closed := subscriber.closed
		hub.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-subscriber.notify:
		case <-ctx.Done():
			return
		case <-hub.ctx.Done():
			return
		}
	}
}

func (hub *scopeEventHub) publish(event ScopeEvent) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	event = cloneScopeEvent(event)
	for _, subscriber := range hub.subscribers {
		subscriber.queue = append(subscriber.queue, event)
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func (hub *scopeEventHub) close() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	hub.closed = true
	for _, subscriber := range hub.subscribers {
		subscriber.closed = true
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func cloneScopeEvent(event ScopeEvent) ScopeEvent {
	clone := event
	if event.ChildIDs != nil {
		clone.ChildIDs = append([]string{}, event.ChildIDs...)
	}
	if event.ToolNames != nil {
		clone.ToolNames = append([]string{}, event.ToolNames...)
	}
	return clone
}

// SubscribeEvents returns a live-only stream of response-scope lifecycle
// events. Subscribe before starting a root turn when no event may be missed.
func (c *ResponseScopeCoordinator) SubscribeEvents(ctx context.Context) <-chan ScopeEvent {
	if c == nil || c.events == nil {
		closed := make(chan ScopeEvent)
		close(closed)
		return closed
	}
	return c.events.subscribe(ctx)
}

func (c *ResponseScopeCoordinator) publishEvent(event ScopeEvent) {
	if c == nil || c.events == nil {
		return
	}
	event.OccurredAt = time.Now().UTC()
	c.events.publish(event)
}
