package agentruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"harness-api/provider"
	"harness-api/storage/inmemory"
)

func TestRunInterruptCancelsProviderAndIsIdempotent(t *testing.T) {
	model := &interruptModel{started: make(chan struct{}, 8), streams: []ModelStream{newBlockingInterruptStream()}}
	runtime, _, _ := newLoopRuntime(t, model, inmemory.NewMessageStorage(), 20)
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "session", TurnID: "turn",
		Message: Message{Type: MessageTypeUser, Content: "wait"},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	model.waitForStart(t)

	if err := run.Interrupt(context.Background(), "caller stopped"); err != nil {
		t.Fatalf("first Run.Interrupt() error = %v", err)
	}
	if err := run.Interrupt(context.Background(), "ignored repeat"); err != nil {
		t.Fatalf("second Run.Interrupt() error = %v, want nil", err)
	}
	model.waitForCancellation(t)

	events := collectRuntimeEvents(t, run)
	if got := countEvent(events, AgentInterrupted); got != 1 {
		t.Fatalf("AgentInterrupted events = %d, want one: %#v", got, events)
	}
	if _, err := run.Result(); !errors.Is(err, ErrRunInterrupted) {
		t.Fatalf("Result() error = %v, want ErrRunInterrupted", err)
	}
	if err := runtime.Interrupt(context.Background(), "session", "turn", "again"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Interrupt() after terminal error = %v, want ErrRunNotFound", err)
	}
}

func TestRuntimeInterruptPersistsPendingToolsInCallOrder(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{
		Type: provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{
			{ID: "call_first", Name: "first", Arguments: map[string]any{}},
			{ID: "call_second", Name: "second", Arguments: map[string]any{}},
		}}},
	}}}}}
	messages := inmemory.NewMessageStorage()
	requests := make(chan ToolRequest, 8)
	results := make(chan ToolResultEnvelope, 8)
	interrupts := make(chan ToolInterrupt, 8)
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: messages, ToolRequests: requests, ToolResults: results,
		ToolInterrupts: interrupts, IDGenerator: incrementingRuntimeIDs{}, MaxSteps: 20,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "go"}})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	_ = receiveToolRequest(t, requests)
	_ = receiveToolRequest(t, requests)

	results <- successfulEnvelope("session", "turn", "call_first", "first", `1`)
	waitForRuntimeEvent(t, run, ToolResultReceived)
	if err := runtime.Interrupt(context.Background(), "session", "turn", "stop"); err != nil {
		t.Fatalf("Runtime.Interrupt() error = %v", err)
	}
	select {
	case interrupt := <-interrupts:
		if interrupt.SessionID != "session" || interrupt.TurnID != "turn" || len(interrupt.CallIDs) != 1 || interrupt.CallIDs[0] != "call_second" {
			t.Fatalf("ToolInterrupt = %#v, want only call_second", interrupt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ToolInterrupt")
	}
	collectRuntimeEvents(t, run)

	stored, err := messages.List(context.Background(), "session")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(stored) != 4 || stored[2].ToolResult == nil || stored[3].ToolResult == nil {
		t.Fatalf("stored messages = %#v, want two tool results", stored)
	}
	if got := stored[2].ToolResult; got.CallID != "call_first" || got.Status != ToolResultSucceeded {
		t.Fatalf("first stored result = %#v, want completed call_first", got)
	}
	if got := stored[3].ToolResult; got.CallID != "call_second" || got.Status != ToolResultInterrupted || got.Error != "stop" {
		t.Fatalf("second stored result = %#v, want interrupted call_second", got)
	}
	if got := len(model.Requests()); got != 1 {
		t.Fatalf("provider calls = %d, want no continuation", got)
	}

	runtime.routeToolResult(successfulEnvelope("session", "turn", "call_second", "second", `2`))
	if got := countEvent(run.Events(), ToolResultReceived); got != 1 {
		t.Fatalf("late ToolResultReceived count = %d, want one", got)
	}
}

func TestRuntimeInterruptIsolatesSessionsAndStartCancellation(t *testing.T) {
	model := &interruptModel{started: make(chan struct{}, 8), streams: []ModelStream{newBlockingInterruptStream(), newBlockingInterruptStream()}}
	runtime, _, _ := newLoopRuntime(t, model, inmemory.NewMessageStorage(), 20)
	startA, cancelA := context.WithCancelCause(context.Background())
	runA, err := runtime.Start(startA, Request{SessionID: "session-a", TurnID: "turn-a", Message: Message{Type: MessageTypeUser, Content: "a"}})
	if err != nil {
		t.Fatalf("Start(A) error = %v", err)
	}
	runB, err := runtime.Start(context.Background(), Request{SessionID: "session-b", TurnID: "turn-b", Message: Message{Type: MessageTypeUser, Content: "b"}})
	if err != nil {
		t.Fatalf("Start(B) error = %v", err)
	}
	model.waitForStarts(t, 2)

	cancelA(errors.New("start context cancelled"))
	model.waitForCancellation(t)
	collectRuntimeEvents(t, runA)
	if runB.Done() || model.cancellationCount() != 1 {
		t.Fatalf("interrupting session A also ended session B: done=%v cancellations=%d", runB.Done(), model.cancellationCount())
	}
	if err := runtime.Interrupt(context.Background(), "session-b", "turn-b", "cleanup"); err != nil {
		t.Fatalf("Interrupt(B) error = %v", err)
	}
	collectRuntimeEvents(t, runB)
	if _, err := runA.Result(); !errors.Is(err, ErrRunInterrupted) {
		t.Fatalf("A Result() error = %v, want ErrRunInterrupted", err)
	}
	if _, err := runB.Result(); !errors.Is(err, ErrRunInterrupted) {
		t.Fatalf("B Result() error = %v, want ErrRunInterrupted after explicit cleanup", err)
	}
}

func TestRuntimeRootCancellationInterruptsActiveRun(t *testing.T) {
	root, cancelRoot := context.WithCancelCause(context.Background())
	defer cancelRoot(nil)
	model := &interruptModel{started: make(chan struct{}, 8), streams: []ModelStream{newBlockingInterruptStream()}}
	requests := make(chan ToolRequest, 1)
	results := make(chan ToolResultEnvelope, 1)
	interrupts := make(chan ToolInterrupt, 1)
	runtime, err := New(root, Config{Model: model, Messages: inmemory.NewMessageStorage(), ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "wait"}})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	model.waitForStart(t)
	cancelRoot(errors.New("runtime stopped"))
	model.waitForCancellation(t)
	events := collectRuntimeEvents(t, run)
	if got := countEvent(events, AgentInterrupted); got != 1 {
		t.Fatalf("AgentInterrupted events = %d, want one: %#v", got, events)
	}
	if _, err := run.Result(); !errors.Is(err, ErrRunInterrupted) {
		t.Fatalf("Result() error = %v, want ErrRunInterrupted", err)
	}
}

func TestStartCancellationPersistsInterruptedPendingTools(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{
		Type: provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{
			{ID: "call_first", Name: "first", Arguments: map[string]any{}},
			{ID: "call_second", Name: "second", Arguments: map[string]any{}},
		}}},
	}}}}}
	messages := inmemory.NewMessageStorage()
	requests := make(chan ToolRequest, 8)
	results := make(chan ToolResultEnvelope, 8)
	interrupts := make(chan ToolInterrupt, 8)
	runtime, err := New(context.Background(), Config{Model: model, Messages: messages, ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts, IDGenerator: incrementingRuntimeIDs{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	start, cancel := context.WithCancelCause(context.Background())
	run, err := runtime.Start(start, Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "go"}})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	_ = receiveToolRequest(t, requests)
	_ = receiveToolRequest(t, requests)
	cancel(errors.New("caller cancelled"))
	select {
	case interrupt := <-interrupts:
		if got, want := interrupt.CallIDs, []string{"call_first", "call_second"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("ToolInterrupt CallIDs = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ToolInterrupt")
	}
	collectRuntimeEvents(t, run)
	stored, err := messages.List(context.Background(), "session")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(stored) != 4 || stored[2].ToolResult.Status != ToolResultInterrupted || stored[3].ToolResult.Status != ToolResultInterrupted {
		t.Fatalf("stored interrupted results = %#v", stored)
	}
}

type interruptModel struct {
	mu      sync.Mutex
	streams []ModelStream
	started chan struct{}
	ctxs    []context.Context
}

func (m *interruptModel) Start(ctx context.Context, _ ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.streams) == 0 {
		return nil, errors.New("unexpected provider call")
	}
	stream := m.streams[0]
	m.streams = m.streams[1:]
	m.ctxs = append(m.ctxs, ctx)
	m.started <- struct{}{}
	return stream, nil
}

func (m *interruptModel) waitForStart(t *testing.T) { m.waitForStarts(t, 1) }

func (m *interruptModel) waitForStarts(t *testing.T, count int) {
	t.Helper()
	for range count {
		select {
		case <-m.started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for provider start")
		}
	}
}

func (m *interruptModel) waitForCancellation(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		m.mu.Lock()
		contexts := append([]context.Context(nil), m.ctxs...)
		m.mu.Unlock()
		for _, ctx := range contexts {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for provider cancellation")
		case <-time.After(time.Millisecond):
		}
	}
}

func (m *interruptModel) cancellationCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, ctx := range m.ctxs {
		if ctx.Err() != nil {
			count++
		}
	}
	return count
}

type blockingInterruptStream struct{}

func newBlockingInterruptStream() blockingInterruptStream { return blockingInterruptStream{} }

func (blockingInterruptStream) Subscribe(ctx context.Context) <-chan provider.StreamEvent {
	channel := make(chan provider.StreamEvent)
	go func() {
		<-ctx.Done()
		close(channel)
	}()
	return channel
}

func (blockingInterruptStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, nil
}
