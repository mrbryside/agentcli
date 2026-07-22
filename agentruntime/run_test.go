package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunIdentityEventsDoneAndResult(t *testing.T) {
	run := newRun("session-1", "turn-1")
	if got := run.SessionID(); got != "session-1" {
		t.Fatalf("SessionID() = %q, want session-1", got)
	}
	if got := run.TurnID(); got != "turn-1" {
		t.Fatalf("TurnID() = %q, want turn-1", got)
	}
	if run.Done() {
		t.Fatal("Done() = true before a terminal event")
	}
	if _, err := run.Result(); !errors.Is(err, ErrRunNotDone) {
		t.Fatalf("Result() error = %v, want ErrRunNotDone", err)
	}

	run.publish(AgentEvent{Type: RunStarted})
	events := run.Events()
	if len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("Events() = %#v, want one sequenced event", events)
	}
	if events[0].SessionID != "session-1" || events[0].TurnID != "turn-1" {
		t.Fatalf("event identity = %#v, want run identity", events[0])
	}

	run.publish(AgentEvent{Type: RunCompleted})
	if !run.Done() {
		t.Fatal("Done() = false after RunCompleted")
	}
	result, err := run.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if !result.Finished || result.SessionID != "session-1" || result.TurnID != "turn-1" {
		t.Fatalf("Result() = %#v, want completed run result", result)
	}
}

func TestRunSubscribeIsLiveOnlyAndClosesAfterTerminal(t *testing.T) {
	run := newRun("session-1", "turn-1")
	run.publish(AgentEvent{Type: RunStarted, Reason: "old"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	subscription := run.Subscribe(ctx)
	if got := subscription.Cursor; got != (EventCursor{SessionID: "session-1", TurnID: "turn-1", Sequence: 1}) {
		t.Fatalf("subscription cursor = %#v", got)
	}
	run.publish(AgentEvent{Type: ProviderEventReceived, Reason: "new"})
	run.publish(AgentEvent{Type: RunCompleted, Reason: "terminal"})

	events := collectRunEvents(t, subscription.Events)
	if len(events) != 2 {
		t.Fatalf("received %d events, want 2: %#v", len(events), events)
	}
	for index, want := range []string{"new", "terminal"} {
		if got := events[index].Reason; got != want {
			t.Fatalf("event %d reason = %q, want %q", index, got, want)
		}
		if got, wantSequence := events[index].Sequence, uint64(index+2); got != wantSequence {
			t.Fatalf("event %d sequence = %d, want %d", index, got, wantSequence)
		}
	}
}

func TestRunSupportsMultipleSubscribers(t *testing.T) {
	run := newRun("session-1", "turn-1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first := run.Subscribe(ctx)
	second := run.Subscribe(ctx)

	run.publish(AgentEvent{Type: RunStarted})
	run.publish(AgentEvent{Type: RunCompleted})

	if got := len(collectRunEvents(t, first.Events)); got != 2 {
		t.Fatalf("first subscriber received %d events, want 2", got)
	}
	if got := len(collectRunEvents(t, second.Events)); got != 2 {
		t.Fatalf("second subscriber received %d events, want 2", got)
	}
}

func TestRunSubscriptionStopsWhenContextIsCancelled(t *testing.T) {
	run := newRun("session-1", "turn-1")
	ctx, cancel := context.WithCancel(context.Background())
	subscription := run.Subscribe(ctx)
	cancel()

	select {
	case _, ok := <-subscription.Events:
		if ok {
			t.Fatal("subscription sent an event after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not close after cancellation")
	}
}

func TestRunEventsAndSubscriptionValuesAreDefensiveCopies(t *testing.T) {
	run := newRun("session-1", "turn-1")
	message := Message{Content: "original"}
	run.publish(AgentEvent{Type: RunStarted, Message: &message})

	history := run.Events()
	history[0].Message.Content = "mutated history"
	if got := run.Events()[0].Message.Content; got != "original" {
		t.Fatalf("Events() retained caller mutation: %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	subscription := run.Subscribe(ctx)
	run.publish(AgentEvent{Type: ProviderEventReceived, Message: &message})
	received := <-subscription.Events
	received.Message.Content = "mutated subscription"
	if got := run.Events()[1].Message.Content; got != "original" {
		t.Fatalf("subscription shared state: %q", got)
	}
}

func TestRunSlowSubscriberDoesNotBlockPublicationOrFastSubscriber(t *testing.T) {
	run := newRun("session-1", "turn-1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	slow := run.Subscribe(ctx)
	fast := run.Subscribe(ctx)
	_ = slow // The slow consumer deliberately never receives.

	const events = 200
	published := make(chan struct{})
	go func() {
		defer close(published)
		for index := 0; index < events; index++ {
			run.publish(AgentEvent{Type: ProviderEventReceived, Reason: "event"})
		}
		run.publish(AgentEvent{Type: RunCompleted})
	}()

	select {
	case <-published:
	case <-ctx.Done():
		t.Fatal("slow subscriber blocked publication")
	}

	if got := len(collectRunEvents(t, fast.Events)); got != events+1 {
		t.Fatalf("fast subscriber received %d events, want %d", got, events+1)
	}
}

func TestRunEventsBetweenValidatesCursorsAndDefensivelyClones(t *testing.T) {
	run := newRun("session-1", "turn-1")
	message := Message{Content: "original"}
	run.publish(AgentEvent{Type: RunStarted, Message: &message})
	run.publish(AgentEvent{Type: ProviderEventReceived})

	events, err := run.EventsBetween(EventCursor{}, EventCursor{SessionID: "session-1", TurnID: "turn-1", Sequence: 1})
	if err != nil || len(events) != 1 {
		t.Fatalf("EventsBetween() = (%#v, %v)", events, err)
	}
	events[0].Message.Content = "mutated"
	if got := run.Events()[0].Message.Content; got != "original" {
		t.Fatalf("EventsBetween() shared history: %q", got)
	}

	invalid := []struct{ after, through EventCursor }{
		{EventCursor{SessionID: "wrong", TurnID: "turn-1"}, EventCursor{SessionID: "session-1", TurnID: "turn-1", Sequence: 1}},
		{EventCursor{}, EventCursor{SessionID: "wrong", TurnID: "turn-1", Sequence: 1}},
		{EventCursor{}, EventCursor{SessionID: "session-1", TurnID: "turn-1", Sequence: 3}},
		{EventCursor{SessionID: "session-1", TurnID: "turn-1", Sequence: 2}, EventCursor{SessionID: "session-1", TurnID: "turn-1", Sequence: 1}},
		{EventCursor{Sequence: 1}, EventCursor{SessionID: "session-1", TurnID: "turn-1", Sequence: 1}},
	}
	for _, test := range invalid {
		if _, err := run.EventsBetween(test.after, test.through); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("EventsBetween(%#v, %#v) error = %v, want ErrInvalidRequest", test.after, test.through, err)
		}
	}
}

func TestRunSubscribeFenceAndBackfillHaveNoGapsOrDuplicates(t *testing.T) {
	run := newRun("session-1", "turn-1")
	const before = 100
	for sequence := 0; sequence < before; sequence++ {
		run.publish(AgentEvent{Type: ProviderEventReceived})
	}

	subscription := run.Subscribe(context.Background())
	// Hold the run lock until a publisher and the backfill reader are both
	// ready to contend for it. Their order after release is deliberately
	// unspecified; the subscription fence must make the combined result exact
	// in either case.
	run.mu.Lock()
	publisherStarted := make(chan struct{})
	backfillStarted := make(chan struct{})
	published := make(chan struct{})
	go func() {
		defer close(published)
		close(publisherStarted)
		for sequence := before; sequence < before*2; sequence++ {
			run.publish(AgentEvent{Type: ProviderEventReceived})
		}
		run.publish(AgentEvent{Type: RunCompleted})
	}()
	type backfillResult struct {
		events []AgentEvent
		err    error
	}
	backfilled := make(chan backfillResult, 1)
	go func() {
		close(backfillStarted)
		events, err := run.EventsBetween(EventCursor{}, subscription.Cursor)
		backfilled <- backfillResult{events: events, err: err}
	}()
	<-publisherStarted
	<-backfillStarted
	run.mu.Unlock()
	backfill := <-backfilled
	if backfill.err != nil {
		t.Fatal(backfill.err)
	}
	<-published
	live := collectRunEvents(t, subscription.Events)
	all := append(backfill.events, live...)
	if len(all) != before*2+1 {
		t.Fatalf("combined events = %d, want %d", len(all), before*2+1)
	}
	for index, event := range all {
		if got, want := event.Sequence, uint64(index+1); got != want {
			t.Fatalf("combined sequence %d = %d, want %d", index, got, want)
		}
	}
}

func TestRunSubscribeAfterTerminalClosesWithoutReplay(t *testing.T) {
	run := newRun("session-1", "turn-1")
	run.publish(AgentEvent{Type: RunStarted})
	run.publish(AgentEvent{Type: RunCompleted})
	subscription := run.Subscribe(context.Background())
	if events := collectRunEvents(t, subscription.Events); len(events) != 0 {
		t.Fatalf("terminal subscription replayed %#v", events)
	}
	backfill, err := run.EventsBetween(EventCursor{}, subscription.Cursor)
	if err != nil || len(backfill) != 2 {
		t.Fatalf("EventsBetween() = (%#v, %v)", backfill, err)
	}
}

func collectRunEvents(t *testing.T, channel <-chan AgentEvent) []AgentEvent {
	t.Helper()
	events := make([]AgentEvent, 0)
	for event := range channel {
		events = append(events, event)
	}
	return events
}
