package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

type runtimeModel struct{}

func (runtimeModel) Start(context.Context, ModelRequest) (ModelStream, error) { return nil, nil }

type blockingStartRuntimeModel struct{ release chan struct{} }

func (m *blockingStartRuntimeModel) Start(ctx context.Context, _ ModelRequest) (ModelStream, error) {
	select {
	case <-m.release:
		return scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}}}}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestNewValidatesConfigAndAppliesDefaults(t *testing.T) {
	requests := make(chan ToolRequest, 1)
	results := make(chan ToolResultEnvelope, 1)
	interrupts := make(chan ToolInterrupt, 1)
	storage := inmemory.NewMessageStorage()

	runtime, err := New(nil, Config{
		Model:          runtimeModel{},
		Messages:       storage,
		ToolRequests:   requests,
		ToolResults:    results,
		ToolInterrupts: interrupts,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if runtime.ctx == nil || runtime.idGenerator == nil || runtime.maxSteps != 20 {
		t.Fatalf("New() defaults = %#v, want context, generator, and max steps 20", runtime)
	}

	tests := []struct {
		name   string
		mutate func(Config) Config
	}{
		{"nil model", func(c Config) Config { c.Model = nil; return c }},
		{"nil storage", func(c Config) Config { c.Messages = nil; return c }},
		{"nil requests", func(c Config) Config { c.ToolRequests = nil; return c }},
		{"nil results", func(c Config) Config { c.ToolResults = nil; return c }},
		{"nil interrupts", func(c Config) Config { c.ToolInterrupts = nil; return c }},
		{"unbuffered requests", func(c Config) Config { c.ToolRequests = make(chan ToolRequest); return c }},
		{"unbuffered results", func(c Config) Config { c.ToolResults = make(chan ToolResultEnvelope); return c }},
		{"unbuffered interrupts", func(c Config) Config { c.ToolInterrupts = make(chan ToolInterrupt); return c }},
	}
	base := Config{Model: runtimeModel{}, Messages: storage, ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(context.Background(), test.mutate(base)); err == nil {
				t.Fatal("New() error = nil, want invalid configuration")
			}
		})
	}
}

func TestRuntimePassesSeparateDefensiveSystemPrompts(t *testing.T) {
	prompts := []string{"skill discovery", "project AGENTS"}
	model := &scriptedRuntimeModel{
		streams: []ModelStream{
			scriptedStream{events: []provider.StreamEvent{{
				Type: provider.StreamCompleted,
				Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
					Content: "done", Finished: true,
				}},
			}}},
		},
	}
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: inmemory.NewMessageStorage(), SystemPrompts: prompts,
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1),
		ToolInterrupts: make(chan ToolInterrupt, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	prompts[0] = "mutated"
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "system-prompts", Message: Message{Type: MessageTypeUser, Content: "go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRuntimeEvents(t, run)
	requests := model.Requests()
	if len(requests) != 1 || !slices.Equal(requests[0].SystemPrompts, []string{"skill discovery", "project AGENTS"}) {
		t.Fatalf("system prompts = %#v", requests)
	}
}

func TestRuntimeResolvesContextRemindersForEveryProviderRound(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
			CompletedTools: []provider.ToolCall{{ID: "call", Name: "tool", Arguments: map[string]any{}}}, Finished: true,
		}}}}},
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}}}}},
	}}
	var resolved []ContextReminderRequest
	reminders := []ContextReminder{{Content: "open work"}}
	messages := inmemory.NewMessageStorage()
	requests := make(chan ToolRequest, 2)
	results := make(chan ToolResultEnvelope, 2)
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: messages, ToolRequests: requests, ToolResults: results, ToolInterrupts: make(chan ToolInterrupt, 2),
		ContextReminderProvider: func(_ context.Context, request ContextReminderRequest) ([]ContextReminder, error) {
			resolved = append(resolved, request)
			return reminders, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	request := receiveToolRequest(t, requests)
	results <- successfulEnvelope(request.SessionID, request.TurnID, "call", "tool", `null`)
	collectRuntimeEvents(t, run)

	if got, want := resolved, []ContextReminderRequest{{SessionID: "session", TurnID: "turn"}, {SessionID: "session", TurnID: "turn"}}; !slices.Equal(got, want) {
		t.Fatalf("resolved requests = %#v, want %#v", got, want)
	}
	providerRequests := model.Requests()
	if len(providerRequests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(providerRequests))
	}
	for index, request := range providerRequests {
		if got := request.ContextReminders; !slices.Equal(got, []ContextReminder{{Content: "open work"}}) {
			t.Fatalf("request %d reminders = %#v", index, got)
		}
	}
	stored, err := messages.List(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range stored {
		if strings.Contains(message.Content, "open work") {
			t.Fatalf("context reminder persisted in messages: %#v", stored)
		}
	}
}

func TestRuntimeCopiesContextRemindersAndFailsOnResolverError(t *testing.T) {
	t.Run("copies returned values", func(t *testing.T) {
		reminders := []ContextReminder{{Content: "original"}}
		model := &mutatingReminderRuntimeModel{reminders: reminders}
		runtime, err := New(context.Background(), Config{
			Model: model, Messages: inmemory.NewMessageStorage(), ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
			ContextReminderProvider: func(context.Context, ContextReminderRequest) ([]ContextReminder, error) { return reminders, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "go"}})
		if err != nil {
			t.Fatal(err)
		}
		collectRuntimeEvents(t, run)
		if reminders[0].Content != "original" {
			t.Fatalf("resolver-owned reminder was mutated: %#v", reminders)
		}
	})

	t.Run("resolver error fails run", func(t *testing.T) {
		model := &scriptedRuntimeModel{}
		messages := inmemory.NewMessageStorage()
		runtime, err := New(context.Background(), Config{
			Model: model, Messages: messages, ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
			ContextReminderProvider: func(context.Context, ContextReminderRequest) ([]ContextReminder, error) {
				return nil, errors.New("reminder unavailable")
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "go"}})
		if err != nil {
			t.Fatal(err)
		}
		collectRuntimeEvents(t, run)
		if _, err := run.Result(); err == nil || !strings.Contains(err.Error(), "reminder unavailable") {
			t.Fatalf("Result() error = %v, want resolver error", err)
		}
		if got := model.Requests(); len(got) != 0 {
			t.Fatalf("provider requests = %#v, want none", got)
		}
		stored, err := messages.List(context.Background(), "session")
		if err != nil {
			t.Fatal(err)
		}
		if len(stored) != 1 || stored[0].Content != "go" {
			t.Fatalf("stored messages = %#v, want only raw user message", stored)
		}
	})
}

func TestRuntimeStartRegistersTurnsAndChecksHistory(t *testing.T) {
	requests := make(chan ToolRequest, 1)
	results := make(chan ToolResultEnvelope, 1)
	interrupts := make(chan ToolInterrupt, 1)
	ids := &deterministicIDGenerator{ids: map[string]string{"turn_": "turn-generated", "msg_": "msg-generated"}}
	model := &blockingStartRuntimeModel{release: make(chan struct{})}
	defer close(model.release)
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: inmemory.NewMessageStorage(), ToolRequests: requests,
		ToolResults: results, ToolInterrupts: interrupts, IDGenerator: ids,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	run, err := runtime.Start(context.Background(), Request{SessionID: "session", Message: Message{Type: MessageTypeUser, Content: "hello"}})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if run.TurnID() != "turn-generated" {
		t.Fatalf("TurnID() = %q, want generated ID", run.TurnID())
	}
	if _, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "next", Message: Message{Type: MessageTypeUser, Content: "next"}}); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("concurrent Start() error = %v, want ErrTurnInProgress", err)
	}
	runtime.unregister(run)

	next, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn-caller", Message: Message{Type: MessageTypeUser, Content: "caller"}})
	if err != nil || next.TurnID() != "turn-caller" {
		t.Fatalf("Start() = (%v, %v), want caller turn", next, err)
	}
	runtime.unregister(next)

	if _, err := runtime.Start(context.Background(), Request{SessionID: "", Message: Message{Type: MessageTypeUser}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid Start() error = %v, want ErrInvalidRequest", err)
	}
}

func TestRuntimeModeChangePublishesToActiveRunsAndStartDoesNotWaitForProvider(t *testing.T) {
	model := &blockingStartRuntimeModel{release: make(chan struct{})}
	requests := make(chan ToolRequest, 4)
	results := make(chan ToolResultEnvelope, 4)
	interrupts := make(chan ToolInterrupt, 4)
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: inmemory.NewMessageStorage(), ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := func(session string) *Run {
		type outcome struct {
			run *Run
			err error
		}
		returned := make(chan outcome, 1)
		go func() {
			run, err := runtime.Start(context.Background(), Request{SessionID: session, TurnID: session + "-turn", Message: Message{Type: MessageTypeUser, Content: "go"}})
			returned <- outcome{run: run, err: err}
		}()
		select {
		case outcome := <-returned:
			if outcome.err != nil {
				t.Fatal(outcome.err)
			}
			return outcome.run
		case <-time.After(time.Second):
			t.Fatal("Runtime.Start waited for blocked Model.Start")
			return nil
		}
	}
	first := start("one")
	second := start("two")
	if err := runtime.SetPermissionMode(permission.CriticalOnly); err != nil {
		t.Fatal(err)
	}
	future := start("future")
	futureEvents := future.Events()
	if len(futureEvents) != 1 || futureEvents[0].Type != RunStarted || futureEvents[0].PermissionMode == nil || futureEvents[0].PermissionMode.Current != permission.CriticalOnly {
		t.Fatalf("future initial event = %#v, want RunStarted with criticalOnly", futureEvents)
	}
	for _, run := range []*Run{first, second} {
		events := run.Events()
		if len(events) != 2 || events[0].Type != RunStarted || events[0].PermissionMode == nil || events[0].PermissionMode.Current != permission.Default {
			t.Fatalf("initial events = %#v, want RunStarted with default mode", events)
		}
		if events[1].Type != PermissionModeChanged || events[1].PermissionMode == nil || events[1].PermissionMode.Previous != permission.Default || events[1].PermissionMode.Current != permission.CriticalOnly {
			t.Fatalf("mode event = %#v", events[1])
		}
	}
	if err := runtime.SetPermissionMode(permission.CriticalOnly); err != nil {
		t.Fatal(err)
	}
	if got := len(first.Events()); got != 2 {
		t.Fatalf("same-mode update appended %d events, want 2", got)
	}
	close(model.release)
	collectRuntimeEvents(t, first)
	collectRuntimeEvents(t, second)
	collectRuntimeEvents(t, future)
}

func TestRuntimeLoopCompletesNoToolRound(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{
		{Type: provider.ContentReceived, Content: "hel"},
		{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "hello", Finished: true}}},
	}}}}
	messages := inmemory.NewMessageStorage()
	runtime, requests, results := newLoopRuntime(t, model, messages, 20)
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	events := collectRuntimeEvents(t, run)
	if got, want := eventTypes(events), []EventType{RunStarted, ProviderEventReceived, ProviderEventReceived, RunCompleted}; !sameEventTypes(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	if got := model.Requests(); len(got) != 1 || len(got[0].Messages) != 1 || got[0].Messages[0].Content != "hi" {
		t.Fatalf("provider requests = %#v, want one request with persisted user message", got)
	}
	stored, err := messages.List(context.Background(), "session")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(stored) != 2 || stored[0].Type != MessageTypeUser || stored[1].Type != MessageTypeAssistant || stored[1].Content != "hello" {
		t.Fatalf("stored messages = %#v, want user then final assistant", stored)
	}
	result, err := run.Result()
	if err != nil || !result.Finished || result.Content != "hello" || result.Steps != 1 {
		t.Fatalf("Result() = (%#v, %v), want completed one-step result", result, err)
	}
	if len(requests) != 0 || len(results) != 0 {
		t.Fatal("no-tool completion touched tool transports")
	}
}

func TestRuntimeStartSubscribedReceivesRunStartedWithImmediateCompletion(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{
		Type:    provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}},
	}}}}}
	runtime, _, _ := newLoopRuntime(t, model, inmemory.NewMessageStorage(), 20)
	run, subscription, err := runtime.StartSubscribed(context.Background(), Request{
		SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "go"},
	})
	if err != nil {
		t.Fatalf("StartSubscribed() error = %v", err)
	}
	if subscription.Cursor != (EventCursor{SessionID: "session", TurnID: "turn"}) {
		t.Fatalf("initial subscription cursor = %#v", subscription.Cursor)
	}
	events := collectRunEvents(t, subscription.Events)
	if got, want := eventTypes(events), []EventType{RunStarted, ProviderEventReceived, RunCompleted}; !sameEventTypes(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	if !run.Done() {
		t.Fatal("run was not complete after terminal subscription closed")
	}
}

func TestRuntimeNextTurnWaitsForTerminalCleanup(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{
			Type: provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
				Content: "second answer", Finished: true,
			}},
		}}},
	}}
	messages := inmemory.NewMessageStorage()
	runtime, _, _ := newLoopRuntime(t, model, messages, 20)
	finished := newRun("session", "first")
	finished.publish(AgentEvent{Type: RunStarted})
	finished.publish(AgentEvent{Type: RunCompleted})
	runtime.mu.Lock()
	runtime.active["session"] = finished
	runtime.mu.Unlock()

	type startOutcome struct {
		run *Run
		err error
	}
	started := make(chan startOutcome, 1)
	go func() {
		run, startErr := runtime.Start(context.Background(), Request{
			SessionID: "session", TurnID: "second", Message: Message{Type: MessageTypeUser, Content: "second"},
		})
		started <- startOutcome{run: run, err: startErr}
	}()
	select {
	case outcome := <-started:
		t.Fatalf("next turn returned before cleanup: (%v, %v)", outcome.run, outcome.err)
	case <-time.After(20 * time.Millisecond):
	}
	finished.finish(runtime)

	var outcome startOutcome
	select {
	case outcome = <-started:
	case <-time.After(time.Second):
		t.Fatal("next turn did not start after cleanup")
	}
	if outcome.err != nil {
		t.Fatalf("next turn error = %v", outcome.err)
	}
	collectRuntimeEvents(t, outcome.run)
	stored, err := messages.List(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].Content != "second" || stored[1].Content != "second answer" {
		t.Fatalf("stored transcript = %#v", stored)
	}
}

func TestRuntimeLoopPersistsToolRoundBeforeDispatchAndContinues(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "call_weather", Name: "weather", Arguments: map[string]any{"city": "Bangkok"}}}, Finished: true}}}}},
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "sunny", Finished: true}}}}},
	}}
	messages := inmemory.NewMessageStorage()
	runtime, requests, results := newLoopRuntime(t, model, messages, 20)
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "weather?"}})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	request := receiveToolRequest(t, requests)
	if request.Call.CallID != "call_weather" || request.Call.Name != "weather" {
		t.Fatalf("ToolRequest = %#v", request)
	}
	stored, err := messages.List(context.Background(), "session")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(stored) != 2 || stored[1].Type != MessageTypeToolCall {
		t.Fatalf("tool call was not persisted before dispatch: %#v", stored)
	}
	results <- ToolResultEnvelope{SessionID: "session", TurnID: "turn", Result: ToolResult{CallID: "call_weather", Name: "weather", Status: ToolResultSucceeded, Output: []byte(`{"forecast":"sunny"}`)}}
	collectRuntimeEvents(t, run)

	providerRequests := model.Requests()
	if len(providerRequests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(providerRequests))
	}
	if got := providerRequests[1].Messages; len(got) != 3 || got[0].Type != MessageTypeUser || got[1].Type != MessageTypeToolCall || got[2].Type != MessageTypeToolResult {
		t.Fatalf("second transcript = %#v, want user, tool call, tool result", got)
	}
	result, err := run.Result()
	if err != nil || result.Content != "sunny" || len(result.ToolResults) != 1 || result.ToolResults[0].CallID != "call_weather" {
		t.Fatalf("Result() = (%#v, %v)", result, err)
	}
}

func TestRuntimeLoopWaitsForEveryToolResultInProviderOrder(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{
			{ID: "call_first", Name: "first", Arguments: map[string]any{}},
			{ID: "call_second", Name: "second", Arguments: map[string]any{}},
		}, Finished: true}}}}},
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}}}}},
	}}
	messages := inmemory.NewMessageStorage()
	runtime, requests, results := newLoopRuntime(t, model, messages, 20)
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "go"}})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	_ = receiveToolRequest(t, requests)
	_ = receiveToolRequest(t, requests)

	results <- successfulEnvelope("session", "turn", "call_second", "second", `2`)
	waitForRuntimeEvent(t, run, ToolResultReceived)
	if run.Done() || len(model.Requests()) != 1 {
		t.Fatalf("after first result Done=%v requests=%d, want active one-provider-call run", run.Done(), len(model.Requests()))
	}
	stored, err := messages.List(context.Background(), "session")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored messages after first result = %#v, want no result batch yet", stored)
	}

	results <- successfulEnvelope("session", "turn", "call_first", "first", `1`)
	collectRuntimeEvents(t, run)
	stored, err = messages.List(context.Background(), "session")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(stored) < 4 || stored[2].ToolResult.CallID != "call_first" || stored[3].ToolResult.CallID != "call_second" {
		t.Fatalf("stored result order = %#v, want call_first then call_second", stored)
	}
	if got := len(model.Requests()); got != 2 {
		t.Fatalf("provider request count = %d, want 2 after both results", got)
	}
}

func TestRuntimeLoopFailsForProviderStorageMalformedResultAndMaxSteps(t *testing.T) {
	tests := []struct {
		name  string
		model *scriptedRuntimeModel
		store storage.MessageStorage
		steps int
		after func(t *testing.T, requests <-chan ToolRequest, results chan<- ToolResultEnvelope)
		want  error
	}{
		{
			name: "provider setup", model: &scriptedRuntimeModel{startErr: errors.New("setup")}, store: inmemory.NewMessageStorage(), steps: 20, want: errors.New("setup"),
		},
		{
			name: "stream failed", model: &scriptedRuntimeModel{streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamFailed, Error: errors.New("stream")}}}}}, store: inmemory.NewMessageStorage(), steps: 20, want: errors.New("stream"),
		},
		{
			name: "storage append", model: &scriptedRuntimeModel{}, store: failingRuntimeStorage{MessageStorage: inmemory.NewMessageStorage(), appendErr: errors.New("append")}, steps: 20, want: errors.New("append"),
		},
		{
			name: "malformed result", model: &scriptedRuntimeModel{streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "call", Name: "tool", Arguments: map[string]any{}}}}}}}}}}, store: inmemory.NewMessageStorage(), steps: 20,
			after: func(t *testing.T, requests <-chan ToolRequest, results chan<- ToolResultEnvelope) {
				request := receiveToolRequest(t, requests)
				results <- ToolResultEnvelope{SessionID: request.SessionID, TurnID: request.TurnID, Result: ToolResult{CallID: "call", Name: "tool"}}
			}, want: storage.ErrInvalidMessage,
		},
		{
			name: "maximum steps", model: &scriptedRuntimeModel{streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "call", Name: "tool", Arguments: map[string]any{}}}}}}}}}}, store: inmemory.NewMessageStorage(), steps: 1,
			after: func(t *testing.T, requests <-chan ToolRequest, results chan<- ToolResultEnvelope) {
				request := receiveToolRequest(t, requests)
				results <- successfulEnvelope(request.SessionID, request.TurnID, "call", "tool", `null`)
			}, want: ErrMaxSteps,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, requests, results := newLoopRuntime(t, test.model, test.store, test.steps)
			run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "go"}})
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if test.after != nil {
				test.after(t, requests, results)
			}
			events := collectRuntimeEvents(t, run)
			if got := countEvent(events, RunFailed); got != 1 {
				t.Fatalf("RunFailed events = %d, want exactly one: %#v", got, events)
			}
			if _, err := run.Result(); err == nil || !strings.Contains(err.Error(), test.want.Error()) {
				t.Fatalf("Result() error = %v, want %v", err, test.want)
			}
		})
	}
}

type scriptedRuntimeModel struct {
	mu       sync.Mutex
	streams  []ModelStream
	requests []ModelRequest
	startErr error
}

func (m *scriptedRuntimeModel) Start(_ context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, ModelRequest{SessionID: request.SessionID, TurnID: request.TurnID, SystemPrompts: append([]string(nil), request.SystemPrompts...), ContextReminders: cloneContextReminders(request.ContextReminders), Messages: storage.CloneMessages(request.Messages), Tools: cloneToolDefinitions(request.Tools)})
	if m.startErr != nil {
		return nil, m.startErr
	}
	if len(m.streams) == 0 {
		return nil, errors.New("unexpected provider call")
	}
	stream := m.streams[0]
	m.streams = m.streams[1:]
	return stream, nil
}

func (m *scriptedRuntimeModel) Requests() []ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	requests := make([]ModelRequest, len(m.requests))
	for index, request := range m.requests {
		requests[index] = ModelRequest{SessionID: request.SessionID, TurnID: request.TurnID, SystemPrompts: append([]string(nil), request.SystemPrompts...), ContextReminders: cloneContextReminders(request.ContextReminders), Messages: storage.CloneMessages(request.Messages), Tools: cloneToolDefinitions(request.Tools)}
	}
	return requests
}

type mutatingReminderRuntimeModel struct{ reminders []ContextReminder }

func (m *mutatingReminderRuntimeModel) Start(_ context.Context, request ModelRequest) (ModelStream, error) {
	if len(request.ContextReminders) != 1 || request.ContextReminders[0].Content != "original" {
		return nil, fmt.Errorf("unexpected reminders: %#v", request.ContextReminders)
	}
	request.ContextReminders[0].Content = "mutated"
	return scriptedStream{events: []provider.StreamEvent{{
		Type:    provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}},
	}}}, nil
}

type scriptedStream struct{ events []provider.StreamEvent }

func (s scriptedStream) Subscribe(ctx context.Context) <-chan provider.StreamEvent {
	channel := make(chan provider.StreamEvent, len(s.events))
	go func() {
		defer close(channel)
		for _, event := range s.events {
			select {
			case channel <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return channel
}

func (s scriptedStream) Result() (provider.StreamResult, error) { return provider.StreamResult{}, nil }

type failingRuntimeStorage struct {
	storage.MessageStorage
	appendErr error
}

func (s failingRuntimeStorage) Append(context.Context, ...storage.Message) error { return s.appendErr }

func newLoopRuntime(t *testing.T, model Model, messages storage.MessageStorage, steps int) (*Runtime, chan ToolRequest, chan ToolResultEnvelope) {
	t.Helper()
	requests := make(chan ToolRequest, 8)
	results := make(chan ToolResultEnvelope, 8)
	interrupts := make(chan ToolInterrupt, 8)
	runtime, err := New(context.Background(), Config{Model: model, Messages: messages, ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts, IDGenerator: incrementingRuntimeIDs{}, MaxSteps: steps})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return runtime, requests, results
}

type incrementingRuntimeIDs struct{}

var runtimeIDCounter struct {
	sync.Mutex
	n int
}

func (incrementingRuntimeIDs) NewID(prefix string) (string, error) {
	runtimeIDCounter.Lock()
	defer runtimeIDCounter.Unlock()
	runtimeIDCounter.n++
	return fmt.Sprintf("%s%d", prefix, runtimeIDCounter.n), nil
}

func collectRuntimeEvents(t *testing.T, run *Run) []AgentEvent {
	t.Helper()
	deadline := time.After(time.Second)
	for !run.Done() {
		select {
		case <-deadline:
			t.Fatalf("run did not terminate: %#v", run.Events())
		case <-time.After(time.Millisecond):
		}
	}
	return run.Events()
}

func receiveToolRequest(t *testing.T, requests <-chan ToolRequest) ToolRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool request")
		return ToolRequest{}
	}
}

func successfulEnvelope(sessionID, turnID, callID, name, output string) ToolResultEnvelope {
	return ToolResultEnvelope{SessionID: sessionID, TurnID: turnID, Result: ToolResult{CallID: callID, Name: name, Status: ToolResultSucceeded, Output: []byte(output)}}
}

func waitForRuntimeEvent(t *testing.T, run *Run, want EventType) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		for _, event := range run.Events() {
			if event.Type == want {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s: %#v", want, run.Events())
		case <-time.After(time.Millisecond):
		}
	}
}

func countEvent(events []AgentEvent, want EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == want {
			count++
		}
	}
	return count
}

func eventTypes(events []AgentEvent) []EventType {
	types := make([]EventType, len(events))
	for index, event := range events {
		types[index] = event.Type
	}
	return types
}

func sameEventTypes(got, want []EventType) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
