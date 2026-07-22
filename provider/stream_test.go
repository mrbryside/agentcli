package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStreamSubscribeReplaysHistoryAndClosesAfterCompletion(t *testing.T) {
	stream := newStream()
	stream.publish(StreamEvent{Type: ContentReceived, Content: "old"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := stream.Subscribe(ctx)
	stream.publish(StreamEvent{Type: ContentReceived, Content: "new"})
	if err := stream.complete(StreamEvent{Type: StreamCompleted}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	events := collectEvents(t, ch)
	if len(events) != 3 {
		t.Fatalf("events = %#v, want history, live, completion", events)
	}
	if events[0].Content != "old" || events[1].Content != "new" || events[2].Type != StreamCompleted {
		t.Fatalf("event order = %#v", events)
	}
	if !stream.Done() {
		t.Fatal("stream should be done")
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
}

func TestStreamSupportsMultipleSubscribers(t *testing.T) {
	stream := newStream()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	first := stream.Subscribe(ctx)
	second := stream.Subscribe(ctx)
	stream.publish(StreamEvent{Type: ContentReceived, Content: "hello"})
	if err := stream.complete(StreamEvent{Type: StreamCompleted}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	firstEvents := collectEvents(t, first)
	secondEvents := collectEvents(t, second)
	if len(firstEvents) != 2 || len(secondEvents) != 2 {
		t.Fatalf("subscriber events = %#v / %#v", firstEvents, secondEvents)
	}
}

func TestStreamFailureIsPublishedBeforeClose(t *testing.T) {
	stream := newStream()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := stream.Subscribe(ctx)
	want := errors.New("connection lost")
	stream.fail(want)

	events := collectEvents(t, ch)
	if len(events) != 1 || events[0].Type != StreamFailed {
		t.Fatalf("events = %#v", events)
	}
	if _, err := stream.Result(); !errors.Is(err, want) {
		t.Fatalf("Result error = %v, want %v", err, want)
	}
}

func TestStreamCompletionFinalizesToolsAndCarriesResultPayload(t *testing.T) {
	stream := newStream()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := stream.Subscribe(ctx)

	stream.publish(StreamEvent{
		Type: ToolCallStarted,
		Tool: &ToolEvent{Index: 0, ID: "call_1", Type: "function", Name: "search"},
	})
	stream.publish(StreamEvent{
		Type: ToolArgumentsReceived,
		Tool: &ToolEvent{Index: 0, Arguments: `{"query":"go"}`},
	})
	if err := stream.complete(StreamEvent{Type: StreamCompleted}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	events := collectEvents(t, ch)
	if len(events) != 4 {
		t.Fatalf("events = %#v, want start, args, tool complete, stream complete", events)
	}
	if events[2].Type != ToolCallCompleted || events[3].Type != StreamCompleted {
		t.Fatalf("terminal event order = %#v", events)
	}
	payload, ok := events[3].Payload.(StreamCompletedPayload)
	if !ok || len(payload.Result.CompletedTools) != 1 || payload.Result.CompletedTools[0].Name != "search" {
		t.Fatalf("completion payload = %#v", events[3].Payload)
	}
}

func collectEvents(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	for event := range ch {
		events = append(events, event)
	}
	return events
}
