package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"harness-api/storage/inmemory"
)

func TestToolResultRouterRoutesBySessionAndTurn(t *testing.T) {
	requests := make(chan ToolRequest, 1)
	results := make(chan ToolResultEnvelope, 4)
	interrupts := make(chan ToolInterrupt, 1)
	runtime, err := New(context.Background(), Config{Model: runtimeModel{}, Messages: inmemory.NewMessageStorage(), ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts})
	if err != nil {
		t.Fatal(err)
	}
	first := registerTestRun(runtime, "one", "turn-one")
	second := registerTestRun(runtime, "two", "turn-two")

	results <- ToolResultEnvelope{SessionID: "two", TurnID: "turn-two", Result: ToolResult{CallID: "two-first"}}
	results <- ToolResultEnvelope{SessionID: "one", TurnID: "turn-one", Result: ToolResult{CallID: "one-first"}}
	results <- ToolResultEnvelope{SessionID: "two", TurnID: "turn-two", Result: ToolResult{CallID: "two-second"}}
	results <- ToolResultEnvelope{SessionID: "unknown", TurnID: "turn", Result: ToolResult{CallID: "discard"}}

	waitForToolResults(t, first, 1)
	waitForToolResults(t, second, 2)
	if got := first.drainToolResults(); len(got) != 1 || got[0].Result.CallID != "one-first" {
		t.Fatalf("first queue = %#v", got)
	}
	if got := second.drainToolResults(); len(got) != 2 || got[0].Result.CallID != "two-first" || got[1].Result.CallID != "two-second" {
		t.Fatalf("second queue = %#v", got)
	}

	results <- ToolResultEnvelope{SessionID: "one", TurnID: "wrong", Result: ToolResult{CallID: "discard"}}
	select {
	case <-first.toolResultsNotify:
		t.Fatal("mismatched turn was routed")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestToolResultRouterFailsAllActiveRunsWhenChannelCloses(t *testing.T) {
	requests := make(chan ToolRequest, 1)
	results := make(chan ToolResultEnvelope, 1)
	interrupts := make(chan ToolInterrupt, 1)
	runtime, err := New(context.Background(), Config{Model: runtimeModel{}, Messages: inmemory.NewMessageStorage(), ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts})
	if err != nil {
		t.Fatal(err)
	}
	first := registerTestRun(runtime, "one", "turn-one")
	second := registerTestRun(runtime, "two", "turn-two")
	close(results)
	waitForDone(t, first)
	waitForDone(t, second)
	if _, err := first.Result(); !errors.Is(err, ErrToolResultsClosed) {
		t.Fatalf("first result error = %v", err)
	}
	if _, err := second.Result(); !errors.Is(err, ErrToolResultsClosed) {
		t.Fatalf("second result error = %v", err)
	}
}

func registerTestRun(runtime *Runtime, sessionID, turnID string) *Run {
	run := newRun(sessionID, turnID)
	runtime.mu.Lock()
	runtime.active[sessionID] = run
	runtime.mu.Unlock()
	return run
}

func waitForToolResults(t *testing.T, run *Run, count int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		run.mu.RLock()
		got := len(run.toolResults)
		run.mu.RUnlock()
		if got >= count {
			return
		}
		select {
		case <-run.toolResultsNotify:
		case <-deadline:
			t.Fatalf("tool results = %d, want %d", got, count)
		}
	}
}

func waitForDone(t *testing.T, run *Run) {
	t.Helper()
	deadline := time.After(time.Second)
	for !run.Done() {
		select {
		case <-deadline:
			t.Fatal("run did not become terminal")
		case <-time.After(time.Millisecond):
		}
	}
}
