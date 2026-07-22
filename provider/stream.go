package provider

import (
	"context"
	"errors"
	"sync"
)

// Stream is the live runtime for an event-sourced provider stream.
type Stream struct {
	mu          sync.RWMutex
	state       StreamState
	subscribers map[uint64]*subscription
	nextID      uint64
	done        bool
	result      StreamResult
	resultSet   bool
	terminalErr error
}

type subscription struct {
	channel chan StreamEvent
	notify  chan struct{}
	queue   []StreamEvent
	closed  bool
}

func newStream() *Stream {
	return &Stream{
		state:       EmptyState(),
		subscribers: make(map[uint64]*subscription),
	}
}

// Subscribe replays the current history and then forwards future events.
func (s *Stream) Subscribe(ctx context.Context) <-chan StreamEvent {
	if ctx == nil {
		ctx = context.Background()
	}

	sub := &subscription{
		channel: make(chan StreamEvent, 16),
		notify:  make(chan struct{}, 1),
	}

	s.mu.Lock()
	id := s.nextID
	s.nextID++
	sub.queue = append(sub.queue, Events(s.state)...)
	sub.closed = s.done
	s.subscribers[id] = sub
	s.mu.Unlock()

	go s.runSubscription(ctx, id, sub)
	return sub.channel
}

func (s *Stream) runSubscription(ctx context.Context, id uint64, sub *subscription) {
	for {
		var (
			event    StreamEvent
			hasEvent bool
			closed   bool
		)

		s.mu.Lock()
		if len(sub.queue) > 0 {
			event = sub.queue[0]
			sub.queue = sub.queue[1:]
			hasEvent = true
		} else if sub.closed {
			closed = true
			delete(s.subscribers, id)
		}
		s.mu.Unlock()

		if hasEvent {
			select {
			case sub.channel <- event:
			case <-ctx.Done():
				s.removeSubscriber(id)
				close(sub.channel)
				return
			}
			continue
		}
		if closed {
			close(sub.channel)
			return
		}

		select {
		case <-sub.notify:
		case <-ctx.Done():
			s.removeSubscriber(id)
			close(sub.channel)
			return
		}
	}
}

func (s *Stream) removeSubscriber(id uint64) {
	s.mu.Lock()
	delete(s.subscribers, id)
	s.mu.Unlock()
}

// Events returns the complete event history accumulated by the stream.
func (s *Stream) Events() []StreamEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Events(s.state)
}

// Done reports whether the stream has reached a terminal state.
func (s *Stream) Done() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.done
}

// Result returns the cached aggregate after the stream has terminated.
func (s *Stream) Result() (StreamResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.done {
		return StreamResult{}, ErrStreamNotDone
	}
	if s.terminalErr != nil {
		return StreamResult{}, s.terminalErr
	}
	if !s.resultSet {
		return StreamResult{}, errors.New("stream completed without a result")
	}
	return cloneResult(s.result), nil
}

func (s *Stream) publish(event StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.state = State(s.state, event)
	s.enqueueLocked(event)
}

func (s *Stream) enqueueLocked(event StreamEvent) {
	for _, sub := range s.subscribers {
		sub.queue = append(sub.queue, cloneEvent(event))
		signal(sub.notify)
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *Stream) fail(err error) {
	if err == nil {
		err = errors.New("stream failed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}

	event := StreamEvent{
		Type:    StreamFailed,
		Error:   err,
		Payload: StreamFailedPayload{Error: err},
	}
	s.state = State(s.state, event)
	s.enqueueLocked(event)
	s.done = true
	s.terminalErr = err
	s.closeSubscribersLocked()
}

func (s *Stream) complete(event StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil
	}

	base := s.state
	pendingEvents, err := pendingToolCompletionEvents(Events(base))
	if err != nil {
		return err
	}
	candidate := base
	for _, pendingEvent := range pendingEvents {
		candidate = State(candidate, pendingEvent)
	}

	candidate = State(candidate, event)
	result, err := Result(Events(candidate))
	if err != nil {
		return err
	}

	finalEvent := cloneEvent(event)
	finalEvent.Payload = StreamCompletedPayload{Result: result}
	for _, pendingEvent := range pendingEvents {
		s.enqueueLocked(pendingEvent)
	}
	// Store only the enriched completion event; the temporary marker was used
	// only to calculate the result.
	next := base
	for _, pendingEvent := range pendingEvents {
		next = State(next, pendingEvent)
	}
	s.state = State(next, finalEvent)
	s.enqueueLocked(finalEvent)
	s.done = true
	s.result = result
	s.resultSet = true
	s.closeSubscribersLocked()
	return nil
}

func (s *Stream) closeSubscribersLocked() {
	for _, sub := range s.subscribers {
		sub.closed = true
		signal(sub.notify)
	}
}
