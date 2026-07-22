package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
)

func TestExecutorRunsWorkersInParallel(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	registry := executorRegistry(t, map[string]Handler{
		"wait": func(_ context.Context, arguments json.RawMessage) (json.RawMessage, error) {
			entered <- string(arguments)
			<-release
			return json.RawMessage(`{"ok":true}`), nil
		},
	})

	requests := make(chan agentruntime.ToolRequest, 2)
	results := make(chan agentruntime.ToolResultEnvelope, 2)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor, err := NewExecutor(registry, 2)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	done := runExecutor(executor, ctx, requests, results, interrupts)

	requests <- toolRequest("session-a", "turn-a", "call-a", "wait", `{"request":"a"}`)
	requests <- toolRequest("session-b", "turn-b", "call-b", "wait", `{"request":"b"}`)
	gotEntered := map[string]bool{waitString(t, entered): true, waitString(t, entered): true}
	if !gotEntered[`{"request":"a"}`] || !gotEntered[`{"request":"b"}`] {
		t.Fatalf("workers entered = %v, want both requests", gotEntered)
	}
	close(release)

	got := make(map[string]agentruntime.ToolResultEnvelope, 2)
	for range 2 {
		result := waitResult(t, results)
		got[result.Result.CallID] = result
	}
	for _, want := range []struct {
		sessionID, turnID, callID string
	}{
		{"session-a", "turn-a", "call-a"},
		{"session-b", "turn-b", "call-b"},
	} {
		result, ok := got[want.callID]
		if !ok {
			t.Fatalf("missing result for %s: %v", want.callID, got)
		}
		if result.SessionID != want.sessionID || result.TurnID != want.turnID || result.Result.CallID != want.callID || result.Result.Name != "wait" || result.Result.Status != agentruntime.ToolResultSucceeded || string(result.Result.Output) != `{"ok":true}` {
			t.Fatalf("result = %+v, want unchanged correlation and success", result)
		}
	}

	cancel()
	waitDone(t, done)
}

func TestExecutorReturnsFailedResults(t *testing.T) {
	registry := executorRegistry(t, map[string]Handler{
		"error": func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("handler failed")
		},
		"panic": func(context.Context, json.RawMessage) (json.RawMessage, error) {
			panic("handler panicked")
		},
	})
	requests := make(chan agentruntime.ToolRequest, 3)
	results := make(chan agentruntime.ToolResultEnvelope, 3)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor, err := NewExecutor(registry, 1)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	done := runExecutor(executor, ctx, requests, results, interrupts)

	requests <- toolRequest("session", "turn", "unknown", "missing", `{}`)
	requests <- toolRequest("session", "turn", "error", "error", `{}`)
	requests <- toolRequest("session", "turn", "panic", "panic", `{}`)

	got := make(map[string]agentruntime.ToolResult)
	for range 3 {
		result := waitResult(t, results).Result
		got[result.CallID] = result
	}
	for _, callID := range []string{"unknown", "error", "panic"} {
		result := got[callID]
		if result.Status != agentruntime.ToolResultFailed || result.Error == "" {
			t.Fatalf("%s result = %+v, want failed result with error", callID, result)
		}
	}

	cancel()
	waitDone(t, done)
}

func TestNewExecutorRejectsInvalidConfiguration(t *testing.T) {
	registry := NewRegistry()
	for _, workers := range []int{0, -1} {
		if _, err := NewExecutor(registry, workers); err == nil {
			t.Fatalf("NewExecutor(workers=%d) error = nil, want rejection", workers)
		}
	}
	if _, err := NewExecutor(nil, 1); err == nil {
		t.Fatal("NewExecutor(nil, 1) error = nil, want rejection")
	}
}

func TestExecutorStopsWhenRequestsClose(t *testing.T) {
	executor, err := NewExecutor(NewRegistry(), 1)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	requests := make(chan agentruntime.ToolRequest)
	results := make(chan agentruntime.ToolResultEnvelope)
	interrupts := make(chan agentruntime.ToolInterrupt)
	close(requests)
	done := runExecutor(executor, context.Background(), requests, results, interrupts)
	waitDone(t, done)
}

func TestExecutorRootContextShutdownCancelsWorkers(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	registry := executorRegistry(t, map[string]Handler{
		"wait": func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		},
	})
	executor, err := NewExecutor(registry, 1)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	requests := make(chan agentruntime.ToolRequest, 1)
	results := make(chan agentruntime.ToolResultEnvelope)
	interrupts := make(chan agentruntime.ToolInterrupt)
	ctx, cancel := context.WithCancel(context.Background())
	done := runExecutor(executor, ctx, requests, results, interrupts)
	requests <- toolRequest("session", "turn", "call", "wait", `{}`)
	waitSignal(t, entered)
	cancel()
	waitSignal(t, cancelled)
	waitDone(t, done)
}

func TestExecutorInterruptCancelsOnlyExactCall(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	registry := executorRegistry(t, map[string]Handler{
		"wait": func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		},
	})
	executor, err := NewExecutor(registry, 1)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	requests := make(chan agentruntime.ToolRequest, 1)
	results := make(chan agentruntime.ToolResultEnvelope, 1)
	interrupts := make(chan agentruntime.ToolInterrupt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runExecutor(executor, ctx, requests, results, interrupts)
	requests <- toolRequest("session-a", "turn", "call", "wait", `{}`)
	waitSignal(t, entered)

	interrupts <- agentruntime.ToolInterrupt{SessionID: "session-b", TurnID: "turn", CallIDs: []string{"call"}, Reason: "wrong session"}
	assertNoSignal(t, cancelled)
	interrupts <- agentruntime.ToolInterrupt{SessionID: "session-a", TurnID: "turn", CallIDs: []string{"call"}, Reason: "user cancelled"}
	waitSignal(t, cancelled)
	result := waitResult(t, results)
	if result.SessionID != "session-a" || result.TurnID != "turn" || result.Result.CallID != "call" || result.Result.Name != "wait" || result.Result.Status != agentruntime.ToolResultInterrupted || result.Result.Error != "user cancelled" {
		t.Fatalf("result = %+v, want matching interrupted envelope", result)
	}

	cancel()
	waitDone(t, done)
}

func executorRegistry(t *testing.T, handlers map[string]Handler) *Registry {
	t.Helper()
	registry := NewRegistry()
	for name, handler := range handlers {
		if err := registry.Register(Tool{
			Definition: agentruntime.ToolDefinition{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)},
			Handler:    handler,
		}); err != nil {
			t.Fatalf("Register(%q) error = %v", name, err)
		}
	}
	return registry
}

func toolRequest(sessionID, turnID, callID, name, arguments string) agentruntime.ToolRequest {
	return agentruntime.ToolRequest{
		SessionID: sessionID,
		TurnID:    turnID,
		Call: agentruntime.ToolCall{
			CallID:    callID,
			Name:      name,
			Arguments: json.RawMessage(arguments),
		},
	}
}

func runExecutor(executor *Executor, ctx context.Context, requests <-chan agentruntime.ToolRequest, results chan<- agentruntime.ToolResultEnvelope, interrupts <-chan agentruntime.ToolInterrupt) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		executor.Run(ctx, requests, results, interrupts)
	}()
	return done
}

func waitResult(t *testing.T, results <-chan agentruntime.ToolResultEnvelope) agentruntime.ToolResultEnvelope {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool result")
		return agentruntime.ToolResultEnvelope{}
	}
}

func waitString(t *testing.T, values <-chan string) string {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for handler")
		return ""
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func assertNoSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
		t.Fatal("unexpected cancellation")
	default:
	}
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for executor shutdown")
	}
}
