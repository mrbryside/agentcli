package agentruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	. "github.com/mrbryside/agentcli/agentruntime"
	openaiadapter "github.com/mrbryside/agentcli/agentruntime/modeladapter/openai"
	provideropenai "github.com/mrbryside/agentcli/provider/openai"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestIntegrationToolRoundTrip(t *testing.T) {
	fixture := newIntegrationSSEFixture(t,
		integrationToolCallStream(integrationToolCall{ID: "call_weather", Name: "weather", Arguments: `{"city":"Bangkok"}`}),
		integrationContentStream("Bangkok is sunny."),
	)
	registry := toolexecution.NewRegistry()
	if err := registry.Register(toolexecution.Tool{
		Definition: ToolDefinition{
			Name: "weather", Description: "Look up the weather",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		},
		Handler: func(_ context.Context, arguments json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"forecast":"sunny","city":"Bangkok"}`), nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	runtime, messages := newIntegrationRuntime(t, fixture, registry, 1)
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "session-tool-round", TurnID: "turn-tool-round",
		Message: Message{Type: MessageTypeUser, Content: "What is the weather in Bangkok?"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := collectIntegrationRunEvents(t, run)

	if got, want := fixture.RequestCount(), 2; got != want {
		t.Fatalf("provider request count = %d, want %d", got, want)
	}
	second := fixture.Request(1)
	assertIntegrationSecondRequest(t, second, []string{"user", "assistant", "tool"}, []string{"call_weather"})

	assertIntegrationEventOrder(t, events, []EventType{
		RunStarted, ProviderEventReceived, ProviderEventReceived, ProviderEventReceived, ProviderEventReceived,
		ToolCallRequested, ToolResultReceived, ProviderEventReceived, ProviderEventReceived,
		RunCompleted,
	})
	for _, event := range events {
		if event.SessionID != run.SessionID() || event.TurnID != run.TurnID() {
			t.Fatalf("event identifiers = (%q, %q), want (%q, %q)", event.SessionID, event.TurnID, run.SessionID(), run.TurnID())
		}
	}
	toolRequest := onlyIntegrationEvent(t, events, ToolCallRequested)
	if toolRequest.ToolRequest == nil || toolRequest.ToolRequest.Call.CallID != "call_weather" {
		t.Fatalf("tool request = %#v, want call_weather", toolRequest.ToolRequest)
	}
	toolResult := onlyIntegrationEvent(t, events, ToolResultReceived)
	if toolResult.ToolResult == nil || toolResult.ToolResult.Result.CallID != "call_weather" || toolResult.ToolResult.Result.Status != ToolResultSucceeded {
		t.Fatalf("tool result = %#v, want successful call_weather", toolResult.ToolResult)
	}

	stored, err := messages.List(context.Background(), run.SessionID())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 4 || stored[0].Type != MessageTypeUser || stored[1].Type != MessageTypeToolCall || stored[2].Type != MessageTypeToolResult || stored[3].Type != MessageTypeAssistant {
		t.Fatalf("stored messages = %#v, want user, tool-call, tool-result, assistant", stored)
	}
	if stored[1].ToolCalls[0].CallID != "call_weather" || stored[2].ToolResult.CallID != "call_weather" {
		t.Fatalf("stored tool correlation = %#v", stored)
	}
	result, err := run.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !result.Finished || result.Content != "Bangkok is sunny." || result.Steps != 2 || len(result.ToolResults) != 1 || result.ToolResults[0].CallID != "call_weather" {
		t.Fatalf("RunResult = %#v", result)
	}
}

func TestIntegrationWaitsForEveryToolResult(t *testing.T) {
	fixture := newIntegrationSSEFixture(t,
		integrationToolCallStream(
			integrationToolCall{ID: "call_first", Name: "first", Arguments: `{}`},
			integrationToolCall{ID: "call_second", Name: "second", Arguments: `{}`},
		),
		integrationContentStream("Both tools completed."),
	)
	firstBarrier := newIntegrationBarrier()
	secondBarrier := newIntegrationBarrier()
	registry := toolexecution.NewRegistry()
	registerBarrierTool(t, registry, "first", firstBarrier, `{"position":"first"}`)
	registerBarrierTool(t, registry, "second", secondBarrier, `{"position":"second"}`)

	runtime, messages := newIntegrationRuntime(t, fixture, registry, 2)
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "session-wait", TurnID: "turn-wait",
		Message: Message{Type: MessageTypeUser, Content: "Run both tools."},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	fixture.WaitForRequests(1)
	firstBarrier.Wait(t)
	secondBarrier.Wait(t)
	subscription := run.Subscribe(context.Background())

	secondBarrier.Release()
	resultEvent := receiveIntegrationEvent(t, subscription.Events, func(event AgentEvent) bool {
		return event.Type == ToolResultReceived && event.ToolResult != nil && event.ToolResult.Result.CallID == "call_second"
	})
	if resultEvent.ToolResult.SessionID != run.SessionID() || resultEvent.ToolResult.TurnID != run.TurnID() {
		t.Fatalf("second result identifiers = %#v", resultEvent.ToolResult)
	}
	if run.Done() || fixture.RequestCount() != 1 {
		t.Fatalf("after one tool result Done=%v providerRequests=%d, want active run with one request", run.Done(), fixture.RequestCount())
	}
	stored, err := messages.List(context.Background(), run.SessionID())
	if err != nil {
		t.Fatalf("List after first result: %v", err)
	}
	if len(stored) != 2 || stored[0].Type != MessageTypeUser || stored[1].Type != MessageTypeToolCall {
		t.Fatalf("stored messages after one result = %#v, want no result batch", stored)
	}

	firstBarrier.Release()
	allEvents := append([]AgentEvent{resultEvent}, collectIntegrationEvents(t, subscription.Events)...)
	fixture.WaitForRequests(2)
	if got, want := fixture.RequestCount(), 2; got != want {
		t.Fatalf("provider request count after both results = %d, want %d", got, want)
	}
	stored, err = messages.List(context.Background(), run.SessionID())
	if err != nil {
		t.Fatalf("List after both results: %v", err)
	}
	if len(stored) != 5 || stored[2].Type != MessageTypeToolResult || stored[3].Type != MessageTypeToolResult || stored[2].ToolResult.CallID != "call_first" || stored[3].ToolResult.CallID != "call_second" {
		t.Fatalf("stored result batch = %#v, want provider call order first then second", stored)
	}
	second := fixture.Request(1)
	assertIntegrationSecondRequest(t, second, []string{"user", "assistant", "tool", "tool"}, []string{"call_first", "call_second"})
	if !containsIntegrationEvent(allEvents, RunCompleted) {
		t.Fatalf("events did not complete after both tools: %#v", allEvents)
	}
	result, err := run.Result()
	if err != nil || !result.Finished || result.Content != "Both tools completed." || len(result.ToolResults) != 2 || result.ToolResults[0].CallID != "call_first" || result.ToolResults[1].CallID != "call_second" {
		t.Fatalf("Result = (%#v, %v)", result, err)
	}
}

func TestIntegrationParallelSessions(t *testing.T) {
	providerGate := newIntegrationGate(2)
	handlerGate := newIntegrationGate(2)
	fixture := newIntegrationSSEFixture(t,
		integrationParallelSessionStream(providerGate),
		integrationParallelSessionStream(providerGate),
		integrationParallelSessionStream(providerGate),
		integrationParallelSessionStream(providerGate),
	)
	registry := toolexecution.NewRegistry()
	err := registry.Register(toolexecution.Tool{
		Definition: ToolDefinition{Name: "parallel", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			if err := handlerGate.Block(ctx); err != nil {
				return nil, err
			}
			return json.RawMessage(`{"ok":true}`), nil
		},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	runtime, messages := newIntegrationRuntime(t, fixture, registry, 2)
	runA := startIntegrationRun(t, runtime, "session-parallel-a", "turn-parallel-a", "parallel-a")
	runB := startIntegrationRun(t, runtime, "session-parallel-b", "turn-parallel-b", "parallel-b")
	providerGate.Wait(t)
	if got := fixture.RequestCount(); got != 2 {
		t.Fatalf("provider requests before release = %d, want both parallel sessions", got)
	}
	providerGate.Release()
	handlerGate.Wait(t)
	handlerGate.Release()

	eventsA := collectIntegrationRunEvents(t, runA)
	eventsB := collectIntegrationRunEvents(t, runB)
	fixture.WaitForRequests(4)
	assertIntegrationSessionOutcome(t, messages, runA, eventsA, "call_parallel-a", "parallel complete: parallel-a")
	assertIntegrationSessionOutcome(t, messages, runB, eventsB, "call_parallel-b", "parallel complete: parallel-b")
}

func TestIntegrationInterruptStopsChain(t *testing.T) {
	blocked := newIntegrationBarrier()
	fixture := newIntegrationSSEFixture(t,
		integrationInterruptSessionStream,
		integrationInterruptSessionStream,
	)
	registry := toolexecution.NewRegistry()
	registerBarrierTool(t, registry, "block", blocked, `{"unreachable":true}`)
	runtime, messages, results := newIntegrationRuntimeWithConfig(t, fixture, registry, 2, inmemory.NewMessageStorage(), 0)

	runA := startIntegrationRun(t, runtime, "session-interrupt-a", "turn-interrupt-a", "interrupt-a")
	fixture.WaitForRequests(1)
	blocked.Wait(t)
	runB := startIntegrationRun(t, runtime, "session-interrupt-b", "turn-interrupt-b", "interrupt-b")
	eventsB := collectIntegrationRunEvents(t, runB)
	if result, err := runB.Result(); err != nil || !result.Finished || result.Content != "session B completed" {
		t.Fatalf("session B Result = (%#v, %v)", result, err)
	}

	if err := runtime.Interrupt(context.Background(), runA.SessionID(), runA.TurnID(), "operator stop"); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	blocked.WaitCanceled(t)
	eventsA := collectIntegrationRunEvents(t, runA)
	if _, err := runA.Result(); !errors.Is(err, ErrRunInterrupted) {
		t.Fatalf("interrupted Result error = %v, want ErrRunInterrupted", err)
	}
	if !containsIntegrationEvent(eventsA, AgentInterrupted) || containsIntegrationEvent(eventsA, RunCompleted) || containsIntegrationEvent(eventsA, RunFailed) {
		t.Fatalf("session A terminal events = %#v", eventsA)
	}
	if !containsIntegrationEvent(eventsB, RunCompleted) || containsIntegrationEvent(eventsB, AgentInterrupted) {
		t.Fatalf("session B events = %#v", eventsB)
	}

	stored, err := messages.List(context.Background(), runA.SessionID())
	if err != nil {
		t.Fatalf("List interrupted transcript: %v", err)
	}
	if len(stored) != 3 || stored[2].Type != MessageTypeToolResult || stored[2].ToolResult.Status != ToolResultInterrupted || stored[2].ToolResult.CallID != "call_interrupt_a" {
		t.Fatalf("interrupted transcript = %#v", stored)
	}
	if got := fixture.RequestCount(); got != 2 {
		t.Fatalf("provider requests = %d, want one interrupted A request and B completion only", got)
	}

	// The executor's canceled handler result and this explicitly injected late
	// result must both be ignored after the terminal transition.
	results <- ToolResultEnvelope{SessionID: runA.SessionID(), TurnID: runA.TurnID(), Result: ToolResult{CallID: "call_interrupt_a", Name: "block", Status: ToolResultSucceeded, Output: json.RawMessage(`{"late":true}`)}}
	if got := runA.Events(); len(got) != len(eventsA) {
		t.Fatalf("late result changed terminal event history from %d to %d events", len(eventsA), len(got))
	}
	storedAfterLate, err := messages.List(context.Background(), runA.SessionID())
	if err != nil || len(storedAfterLate) != len(stored) {
		t.Fatalf("late result changed transcript = (%#v, %v)", storedAfterLate, err)
	}
}

func TestIntegrationSerializesTurnsWithinSession(t *testing.T) {
	providerBarrier := newIntegrationBarrier()
	fixture := newIntegrationSSEFixture(t,
		integrationContentAfterBarrier(providerBarrier, "first answer"),
		integrationContentStream("second answer"),
	)
	runtime, _ := newIntegrationRuntime(t, fixture, toolexecution.NewRegistry(), 1)
	runOne := startIntegrationRun(t, runtime, "session-serialized", "turn-one", "first question")
	providerBarrier.Wait(t)
	if _, err := runtime.Start(context.Background(), Request{SessionID: "session-serialized", TurnID: "turn-two", Message: Message{Type: MessageTypeUser, Content: "second question"}}); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("second Start error = %v, want ErrTurnInProgress", err)
	}
	providerBarrier.Release()
	_ = collectIntegrationRunEvents(t, runOne)
	if _, err := runOne.Result(); err != nil {
		t.Fatalf("first Result: %v", err)
	}

	runTwo := startIntegrationRun(t, runtime, "session-serialized", "turn-two", "second question")
	_ = collectIntegrationRunEvents(t, runTwo)
	fixture.WaitForRequests(2)
	request := fixture.Request(1)
	assertIntegrationRequestMessages(t, request, []string{"user", "assistant", "user"}, []string{"first question", "first answer", "second question"})
	if result, err := runTwo.Result(); err != nil || !result.Finished || result.Content != "second answer" {
		t.Fatalf("second Result = (%#v, %v)", result, err)
	}
}

func TestIntegrationFailurePaths(t *testing.T) {
	t.Run("provider setup failure", func(t *testing.T) {
		fixture := newIntegrationSSEFixture(t, func(writer http.ResponseWriter, _ *http.Request, _ map[string]any) {
			http.Error(writer, "fixture setup failure", http.StatusInternalServerError)
		})
		runtime, _ := newIntegrationRuntime(t, fixture, toolexecution.NewRegistry(), 1)
		run := startIntegrationRun(t, runtime, "session-setup-failure", "turn-setup-failure", "fail setup")
		assertIntegrationInfrastructureFailure(t, run)
		if got := fixture.RequestCount(); got != 1 {
			t.Fatalf("provider request count = %d, want 1", got)
		}
	})

	t.Run("provider stream failure", func(t *testing.T) {
		fixture := newIntegrationSSEFixture(t, func(writer http.ResponseWriter, _ *http.Request, _ map[string]any) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(writer, "data: not-json\n\n")
			writer.(http.Flusher).Flush()
		})
		runtime, _ := newIntegrationRuntime(t, fixture, toolexecution.NewRegistry(), 1)
		run := startIntegrationRun(t, runtime, "session-stream-failure", "turn-stream-failure", "fail stream")
		assertIntegrationInfrastructureFailure(t, run)
	})

	t.Run("storage failure", func(t *testing.T) {
		fixture := newIntegrationSSEFixture(t)
		appendErr := errors.New("fixture append failure")
		messages := failingIntegrationStorage{MessageStorage: inmemory.NewMessageStorage(), appendErr: appendErr}
		runtime, _, _ := newIntegrationRuntimeWithConfig(t, fixture, toolexecution.NewRegistry(), 1, messages, 0)
		run := startIntegrationRun(t, runtime, "session-storage-failure", "turn-storage-failure", "fail storage")
		events := collectIntegrationRunEvents(t, run)
		if _, err := run.Result(); !errors.Is(err, appendErr) {
			t.Fatalf("storage Result error = %v, want wrapped %v", err, appendErr)
		}
		if containsIntegrationEvent(events, RunCompleted) || !containsIntegrationEvent(events, RunFailed) || fixture.RequestCount() != 0 {
			t.Fatalf("storage failure events=%#v providerRequests=%d", events, fixture.RequestCount())
		}
	})

	t.Run("maximum steps", func(t *testing.T) {
		fixture := newIntegrationSSEFixture(t, integrationToolCallStream(integrationToolCall{ID: "call_steps", Name: "steps", Arguments: `{}`}))
		registry := toolexecution.NewRegistry()
		registerBarrierTool(t, registry, "steps", releasedIntegrationBarrier(), `{"ok":true}`)
		runtime, _, _ := newIntegrationRuntimeWithConfig(t, fixture, registry, 1, inmemory.NewMessageStorage(), 1)
		run := startIntegrationRun(t, runtime, "session-max-steps", "turn-max-steps", "max steps")
		events := collectIntegrationRunEvents(t, run)
		if _, err := run.Result(); !errors.Is(err, ErrMaxSteps) {
			t.Fatalf("max steps Result error = %v, want ErrMaxSteps", err)
		}
		if !containsIntegrationEvent(events, RunFailed) || fixture.RequestCount() != 1 {
			t.Fatalf("max steps events=%#v providerRequests=%d", events, fixture.RequestCount())
		}
	})

	t.Run("handler failure returns to provider", func(t *testing.T) {
		fixture := newIntegrationSSEFixture(t,
			integrationToolCallStream(integrationToolCall{ID: "call_handler_failure", Name: "fail", Arguments: `{}`}),
			integrationContentStream("provider recovered from tool failure"),
		)
		registry := toolexecution.NewRegistry()
		err := registry.Register(toolexecution.Tool{
			Definition: ToolDefinition{Name: "fail", InputSchema: json.RawMessage(`{"type":"object"}`)},
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return nil, errors.New("handler failed")
			},
		})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		runtime, messages := newIntegrationRuntime(t, fixture, registry, 1)
		run := startIntegrationRun(t, runtime, "session-handler-failure", "turn-handler-failure", "recover tool failure")
		_ = collectIntegrationRunEvents(t, run)
		if result, err := run.Result(); err != nil || !result.Finished || len(result.ToolResults) != 1 || result.ToolResults[0].Status != ToolResultFailed {
			t.Fatalf("handler recovery Result = (%#v, %v)", result, err)
		}
		fixture.WaitForRequests(2)
		second := fixture.Request(1)
		assertIntegrationSecondRequest(t, second, []string{"user", "assistant", "tool"}, []string{"call_handler_failure"})
		requestMessages := second["messages"].([]any)
		if got := requestMessages[2].(map[string]any)["content"]; got != `{"status":"failed","error":"handler failed"}` {
			t.Fatalf("failed tool provider content = %#v", got)
		}
		stored, err := messages.List(context.Background(), run.SessionID())
		if err != nil || len(stored) != 4 || stored[2].ToolResult.Status != ToolResultFailed {
			t.Fatalf("handler recovery transcript = (%#v, %v)", stored, err)
		}
	})
}

func newIntegrationRuntime(t testing.TB, fixture *integrationSSEFixture, registry *toolexecution.Registry, workers int) (*Runtime, storage.MessageStorage) {
	t.Helper()
	runtime, messages, _ := newIntegrationRuntimeWithConfig(t, fixture, registry, workers, inmemory.NewMessageStorage(), 0)
	return runtime, messages
}

func newIntegrationRuntimeWithConfig(t testing.TB, fixture *integrationSSEFixture, registry *toolexecution.Registry, workers int, messages storage.MessageStorage, maxSteps int) (*Runtime, storage.MessageStorage, chan ToolResultEnvelope) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan ToolRequest, 16)
	results := make(chan ToolResultEnvelope, 16)
	interrupts := make(chan ToolInterrupt, 16)
	executor, err := toolexecution.NewExecutor(registry, workers)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	executorDone := make(chan error, 1)
	go func() { executorDone <- executor.Run(ctx, requests, results, interrupts) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-executorDone:
			if err != nil {
				t.Errorf("Executor.Run: %v", err)
			}
		case <-time.After(integrationTimeout):
			t.Error("executor did not stop after cancellation")
		}
	})

	model := openaiadapter.New(provideropenai.NewProvider(provideropenai.Config{URL: fixture.URL(), APIKey: "fixture-key"}), openaiadapter.Config{Model: "fixture-model"})
	runtime, err := New(ctx, Config{
		Model: model, Messages: messages, Tools: registry.Definitions(),
		ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts,
		MaxSteps: maxSteps,
	})
	if err != nil {
		t.Fatalf("New Runtime: %v", err)
	}
	return runtime, messages, results
}

type failingIntegrationStorage struct {
	storage.MessageStorage
	appendErr error
}

func (s failingIntegrationStorage) Append(context.Context, ...storage.Message) error {
	return s.appendErr
}

func startIntegrationRun(t testing.TB, runtime *Runtime, sessionID, turnID, content string) *Run {
	t.Helper()
	run, err := runtime.Start(context.Background(), Request{
		SessionID: sessionID,
		TurnID:    turnID,
		Message:   Message{Type: MessageTypeUser, Content: content},
	})
	if err != nil {
		t.Fatalf("Start %s/%s: %v", sessionID, turnID, err)
	}
	return run
}

func releasedIntegrationBarrier() *integrationBarrier {
	barrier := newIntegrationBarrier()
	barrier.Release()
	return barrier
}

func integrationParallelSessionStream(providerGate *integrationGate) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(writer http.ResponseWriter, request *http.Request, decoded map[string]any) {
		user := integrationRequestUserContent(decoded)
		messages := decoded["messages"].([]any)
		if len(messages) == 1 {
			if err := providerGate.Block(request.Context()); err != nil {
				return
			}
			integrationToolCallStream(integrationToolCall{ID: "call_" + user, Name: "parallel", Arguments: `{}`})(writer, request, decoded)
			return
		}
		integrationContentStream("parallel complete: "+user)(writer, request, decoded)
	}
}

func integrationInterruptSessionStream(writer http.ResponseWriter, request *http.Request, decoded map[string]any) {
	if integrationRequestUserContent(decoded) == "interrupt-a" {
		integrationToolCallStream(integrationToolCall{ID: "call_interrupt_a", Name: "block", Arguments: `{}`})(writer, request, decoded)
		return
	}
	integrationContentStream("session B completed")(writer, request, decoded)
}

func integrationContentAfterBarrier(barrier *integrationBarrier, content string) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(writer http.ResponseWriter, request *http.Request, decoded map[string]any) {
		if err := barrier.Block(request.Context()); err != nil {
			return
		}
		integrationContentStream(content)(writer, request, decoded)
	}
}

func integrationRequestUserContent(request map[string]any) string {
	messages, _ := request["messages"].([]any)
	for _, item := range messages {
		message, _ := item.(map[string]any)
		if message["role"] == "user" {
			content, _ := message["content"].(string)
			return content
		}
	}
	return ""
}

func assertIntegrationSessionOutcome(t testing.TB, messages storage.MessageStorage, run *Run, events []AgentEvent, callID, content string) {
	t.Helper()
	if result, err := run.Result(); err != nil || !result.Finished || result.Content != content || len(result.ToolResults) != 1 || result.ToolResults[0].CallID != callID {
		t.Fatalf("%s Result = (%#v, %v)", run.SessionID(), result, err)
	}
	for _, event := range events {
		if event.SessionID != run.SessionID() || event.TurnID != run.TurnID() {
			t.Fatalf("%s received cross-session event %#v", run.SessionID(), event)
		}
	}
	stored, err := messages.List(context.Background(), run.SessionID())
	if err != nil {
		t.Fatalf("List %s: %v", run.SessionID(), err)
	}
	if len(stored) != 4 || stored[0].Type != MessageTypeUser || stored[1].Type != MessageTypeToolCall || stored[2].Type != MessageTypeToolResult || stored[3].Type != MessageTypeAssistant || stored[1].ToolCalls[0].CallID != callID || stored[2].ToolResult.CallID != callID {
		t.Fatalf("%s transcript = %#v", run.SessionID(), stored)
	}
}

func assertIntegrationRequestMessages(t testing.TB, request map[string]any, roles, contents []string) {
	t.Helper()
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) != len(roles) || len(roles) != len(contents) {
		t.Fatalf("request messages = %#v", request["messages"])
	}
	for index, item := range messages {
		message, ok := item.(map[string]any)
		if !ok || message["role"] != roles[index] || message["content"] != contents[index] {
			t.Fatalf("request message %d = %#v, want role/content %q/%q", index, item, roles[index], contents[index])
		}
	}
}

func assertIntegrationInfrastructureFailure(t testing.TB, run *Run) {
	t.Helper()
	events := collectIntegrationRunEvents(t, run)
	if _, err := run.Result(); err == nil {
		t.Fatal("Result error = nil, want infrastructure failure")
	}
	if !containsIntegrationEvent(events, RunFailed) || containsIntegrationEvent(events, RunCompleted) || containsIntegrationEvent(events, ToolResultReceived) {
		t.Fatalf("infrastructure failure events = %#v", events)
	}
}

func collectIntegrationRunEvents(t testing.TB, run *Run) []AgentEvent {
	t.Helper()
	deadline := time.NewTimer(integrationTimeout)
	defer deadline.Stop()
	for !run.Done() {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for run completion: %#v", run.Events())
			return nil
		case <-time.After(time.Millisecond):
		}
	}
	return run.Events()
}

func registerBarrierTool(t testing.TB, registry *toolexecution.Registry, name string, barrier *integrationBarrier, output string) {
	t.Helper()
	err := registry.Register(toolexecution.Tool{
		Definition: ToolDefinition{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			if err := barrier.Block(ctx); err != nil {
				return nil, err
			}
			return json.RawMessage(output), nil
		},
	})
	if err != nil {
		t.Fatalf("Register %q: %v", name, err)
	}
}

func assertIntegrationSecondRequest(t testing.TB, request map[string]any, wantRoles, wantCallIDs []string) {
	t.Helper()
	messages, ok := request["messages"].([]any)
	if !ok {
		t.Fatalf("request messages = %#v", request["messages"])
	}
	if len(messages) != len(wantRoles) {
		t.Fatalf("request message count = %d, want %d: %#v", len(messages), len(wantRoles), messages)
	}
	var assistantCallIDs, resultCallIDs []string
	for index, item := range messages {
		message, ok := item.(map[string]any)
		if !ok || message["role"] != wantRoles[index] {
			t.Fatalf("request message %d = %#v, want role %q", index, item, wantRoles[index])
		}
		if calls, ok := message["tool_calls"].([]any); ok {
			for _, call := range calls {
				callMap := call.(map[string]any)
				assistantCallIDs = append(assistantCallIDs, callMap["id"].(string))
			}
		}
		if toolCallID, ok := message["tool_call_id"].(string); ok && toolCallID != "" {
			resultCallIDs = append(resultCallIDs, toolCallID)
		}
	}
	if len(assistantCallIDs) != len(wantCallIDs) || len(resultCallIDs) != len(wantCallIDs) {
		t.Fatalf("assistant calls = %v; tool result calls = %v; want %v", assistantCallIDs, resultCallIDs, wantCallIDs)
	}
	for index := range wantCallIDs {
		if assistantCallIDs[index] != wantCallIDs[index] || resultCallIDs[index] != wantCallIDs[index] {
			t.Fatalf("assistant calls = %v; tool result calls = %v; want %v", assistantCallIDs, resultCallIDs, wantCallIDs)
		}
	}
}

func assertIntegrationEventOrder(t testing.TB, events []AgentEvent, want []EventType) {
	t.Helper()
	got := make([]EventType, len(events))
	for index, event := range events {
		got[index] = event.Type
	}
	if len(got) != len(want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("event types = %v, want %v", got, want)
		}
	}
}

func onlyIntegrationEvent(t testing.TB, events []AgentEvent, want EventType) AgentEvent {
	t.Helper()
	var matches []AgentEvent
	for _, event := range events {
		if event.Type == want {
			matches = append(matches, event)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("%s events = %d, want one: %#v", want, len(matches), events)
	}
	return matches[0]
}

func containsIntegrationEvent(events []AgentEvent, want EventType) bool {
	for _, event := range events {
		if event.Type == want {
			return true
		}
	}
	return false
}
