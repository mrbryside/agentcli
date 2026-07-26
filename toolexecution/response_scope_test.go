package toolexecution

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
)

func TestResponseScopeDefersLatestCandidateUntilAllCallbacksFinish(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch-1")

	var (
		mu       sync.Mutex
		received []string
	)
	handler := func(_ context.Context, arguments json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		received = append(received, string(arguments))
		mu.Unlock()
		return json.RawMessage(`{"sent":true}`), nil
	}
	rootOutput, err := coordinator.StageEndResponseScope(context.Background(), scopeToolRequest("root-turn", `{"message":"early"}`), handler)
	if err != nil {
		t.Fatal(err)
	}
	assertDeferredScopeResult(t, rootOutput, 1, 1, "scheduled")

	coordinator.FinishTurn("session", "root-turn")
	if got := snapshotStrings(&mu, received); len(got) != 0 {
		t.Fatalf("handler calls after root turn = %v, want none", got)
	}

	reservation, err := coordinator.ReserveCallbackTurn("session", "callback-turn", "child", "child-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	reservation.Commit()
	callbackOutput, err := coordinator.StageEndResponseScope(context.Background(), scopeToolRequest("callback-turn", `{"message":"final"}`), handler)
	if err != nil {
		t.Fatal(err)
	}
	assertDeferredScopeResult(t, callbackOutput, 0, 1, "replaced")
	if got := snapshotStrings(&mu, received); len(got) != 0 {
		t.Fatalf("handler calls before callback turn finishes = %v, want none", got)
	}

	coordinator.FinishTurn("session", "callback-turn")
	if got := snapshotStrings(&mu, received); len(got) != 1 || got[0] != `{"message":"final"}` {
		t.Fatalf("handler calls = %v, want latest candidate once", got)
	}
	if _, err := coordinator.ReserveCallbackTurn("session", "late-replay", "child", "child-turn-1"); err == nil {
		t.Fatal("late callback replay reopened an ended response scope")
	}
	if got := snapshotStrings(&mu, received); len(got) != 1 {
		t.Fatalf("handler calls after late replay = %v, want exactly one", got)
	}

	if err := coordinator.BeginRootTurn("session", "new-root"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "new-root", "child", "new-dispatch")
	coordinator.FinishTurn("session", "new-root")
	if _, err := coordinator.ReserveCallbackTurn("session", "late-replay-with-new-work", "child", "child-turn-1"); err == nil {
		t.Fatal("late replay from ended scope consumed newer response-scope work")
	}
	newCallback, err := coordinator.ReserveCallbackTurn("session", "new-callback", "child", "child-turn-2")
	if err != nil {
		t.Fatalf("new callback after late replay error = %v", err)
	}
	newCallback.Commit()
}

func TestResponseScopeFollowUpReopensBarrierAndCallbackReplayDoesNotCloseIt(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch-1")
	coordinator.FinishTurn("session", "root-turn")

	first, err := coordinator.ReserveCallbackTurn("session", "callback-1", "child", "child-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	first.Commit()
	coordinator.RegisterDispatch("session", "callback-1", "child", "follow-up")

	calls := 0
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}
	if _, err := coordinator.StageEndResponseScope(context.Background(), scopeToolRequest("callback-1", `{"message":"waiting"}`), handler); err != nil {
		t.Fatal(err)
	}
	coordinator.FinishTurn("session", "callback-1")
	if calls != 0 {
		t.Fatalf("handler calls = %d, want none with accepted follow-up pending", calls)
	}

	replay, err := coordinator.ReserveCallbackTurn("session", "callback-replay", "child", "child-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	replay.Commit()
	if _, err := coordinator.StageEndResponseScope(context.Background(), scopeToolRequest("callback-replay", `{"message":"replay"}`), handler); err != nil {
		t.Fatal(err)
	}
	coordinator.FinishTurn("session", "callback-replay")
	if calls != 0 {
		t.Fatalf("handler calls = %d, replay closed a newer pending dispatch", calls)
	}

	second, err := coordinator.ReserveCallbackTurn("session", "callback-2", "child", "child-turn-2")
	if err != nil {
		t.Fatal(err)
	}
	second.Commit()
	if _, err := coordinator.StageEndResponseScope(context.Background(), scopeToolRequest("callback-2", `{"message":"done after failed or incomplete callback"}`), handler); err != nil {
		t.Fatal(err)
	}
	coordinator.FinishTurn("session", "callback-2")
	if calls != 1 {
		t.Fatalf("handler calls = %d, want exactly one after terminal callback", calls)
	}
}

func TestResponseScopeCallbackReservationRollbackRestoresPendingDispatch(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch-1")
	coordinator.FinishTurn("session", "root-turn")

	reservation, err := coordinator.ReserveCallbackTurn("session", "rejected-turn", "child", "child-turn")
	if err != nil {
		t.Fatal(err)
	}
	reservation.Rollback("child", "child-turn")

	retry, err := coordinator.ReserveCallbackTurn("session", "accepted-turn", "child", "child-turn")
	if err != nil {
		t.Fatalf("ReserveCallbackTurn() after rollback error = %v", err)
	}
	retry.Commit()
}

func TestResponseScopeCleanupRunsBeforeDeferredHandlersAndSeesTouchedChildren(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	scopeEvents := coordinator.SubscribeEvents(context.Background())
	var events []string
	coordinator.SetCleanup(func(_ context.Context, sessionID, scopeID string, children []string) {
		events = append(events, "cleanup:"+sessionID+":"+scopeID+":"+strings.Join(children, ","))
	})
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch")
	callback, err := coordinator.ReserveCallbackTurn("session", "callback-turn", "child", "child-turn")
	if err != nil {
		t.Fatal(err)
	}
	callback.Commit()
	if _, err := coordinator.StageEndResponseScope(context.Background(), scopeToolRequest("root-turn", `{}`), func(context.Context, json.RawMessage) (json.RawMessage, error) {
		events = append(events, "handler")
		return json.RawMessage(`{}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	coordinator.FinishTurn("session", "root-turn")
	coordinator.FinishTurn("session", "callback-turn")
	if got, want := strings.Join(events, "|"), "cleanup:session:root-turn:child|handler"; got != want {
		t.Fatalf("scope end order = %q, want %q", got, want)
	}
	preEnd := <-scopeEvents
	end := <-scopeEvents
	if preEnd.Type != PreEndScope || end.Type != EndScope {
		t.Fatalf("scope event types = %q, %q; want %q, %q", preEnd.Type, end.Type, PreEndScope, EndScope)
	}
	for _, event := range []ScopeEvent{preEnd, end} {
		if event.SessionID != "session" || event.ScopeID != "root-turn" || event.TriggerTurnID != "callback-turn" {
			t.Fatalf("scope event correlation = %+v", event)
		}
		if got := strings.Join(event.ChildIDs, ","); got != "child" {
			t.Fatalf("scope event children = %q, want child", got)
		}
		if got := strings.Join(event.ToolNames, ","); got != "report" {
			t.Fatalf("scope event tools = %q, want report", got)
		}
		if event.OccurredAt.IsZero() {
			t.Fatal("scope event occurred_at is zero")
		}
	}
}

func TestScopeEventsBracketCleanupAndEndScopeHandlers(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	scopeEvents := coordinator.SubscribeEvents(context.Background())
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	handlerCalled := make(chan struct{})
	finished := make(chan struct{})
	coordinator.SetCleanup(func(context.Context, string, string, []string) {
		close(cleanupStarted)
		<-releaseCleanup
	})
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.StageEndResponseScope(context.Background(), scopeToolRequest("turn", `{}`), func(context.Context, json.RawMessage) (json.RawMessage, error) {
		close(handlerCalled)
		return json.RawMessage(`{}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	go func() {
		coordinator.FinishTurn("session", "turn")
		close(finished)
	}()

	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("scope cleanup did not start")
	}
	select {
	case event := <-scopeEvents:
		if event.Type != PreEndScope {
			t.Fatalf("first scope event = %q, want %q", event.Type, PreEndScope)
		}
	case <-time.After(time.Second):
		t.Fatal("PreEndScope was not emitted before blocked cleanup")
	}
	select {
	case event := <-scopeEvents:
		t.Fatalf("scope event %q arrived before cleanup completed", event.Type)
	default:
	}
	select {
	case <-handlerCalled:
		t.Fatal("EndResponseScope handler ran before cleanup completed")
	default:
	}

	close(releaseCleanup)
	select {
	case <-handlerCalled:
	case <-time.After(time.Second):
		t.Fatal("EndResponseScope handler did not run after cleanup")
	}
	select {
	case event := <-scopeEvents:
		if event.Type != EndScope {
			t.Fatalf("second scope event = %q, want %q", event.Type, EndScope)
		}
	case <-time.After(time.Second):
		t.Fatal("EndScope was not emitted after handler execution")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("FinishTurn did not return")
	}
}

func TestResponseScopeChildExclusiveRejectsAnotherLiveScopeReference(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	var exclusive bool
	coordinator.SetCleanup(func(_ context.Context, sessionID, scopeID string, children []string) {
		exclusive = coordinator.ChildExclusiveToScope(sessionID, scopeID, children[0])
	})
	if err := coordinator.BeginRootTurn("session", "first"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "first", "child", "first-dispatch")
	if err := coordinator.BeginRootTurn("session", "second"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "second", "child", "second-dispatch")
	firstCallback, err := coordinator.ReserveCallbackTurn("session", "first-callback", "child", "first-child-turn")
	if err != nil {
		t.Fatal(err)
	}
	firstCallback.Commit()
	secondCallback, err := coordinator.ReserveCallbackTurn("session", "second-callback", "child", "second-child-turn")
	if err != nil {
		t.Fatal(err)
	}
	secondCallback.Commit()
	coordinator.FinishTurn("session", "first")
	coordinator.FinishTurn("session", "first-callback")
	if exclusive {
		t.Fatal("child referenced by another live scope was reported exclusive")
	}
}

func TestResponseScopeCleanupFailureDoesNotSuppressDeferredHandler(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	coordinator.SetCleanup(func(context.Context, string, string, []string) {
		panic("cleanup failed")
	})
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, err := coordinator.StageEndResponseScope(context.Background(), scopeToolRequest("turn", `{}`), func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	coordinator.FinishTurn("session", "turn")
	if calls != 1 {
		t.Fatalf("deferred handler calls = %d, want one after cleanup failure", calls)
	}
}

func TestResponseScopeEndEventSurvivesCleanupAndHandlerPanics(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	scopeEvents := coordinator.SubscribeEvents(context.Background())
	coordinator.SetCleanup(func(context.Context, string, string, []string) {
		panic("cleanup failed")
	})
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.StageEndResponseScope(context.Background(), scopeToolRequest("turn", `{}`), func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic("handler failed")
	}); err != nil {
		t.Fatal(err)
	}
	coordinator.FinishTurn("session", "turn")
	if preEnd, end := <-scopeEvents, <-scopeEvents; preEnd.Type != PreEndScope || end.Type != EndScope {
		t.Fatalf("scope events after panics = %q, %q", preEnd.Type, end.Type)
	}
}

func TestResponseScopeRolledBackDispatchIsNotCleanupCandidate(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	var children []string
	coordinator.SetCleanup(func(_ context.Context, _, _ string, ids []string) {
		children = append(children, ids...)
	})
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	rollback := coordinator.RegisterDispatch("session", "turn", "child", "dispatch")
	rollback()
	coordinator.FinishTurn("session", "turn")
	if len(children) != 0 {
		t.Fatalf("rolled-back dispatch cleanup candidates = %v", children)
	}
}

func TestScopeEventStreamClosesWithCoordinatorContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := NewResponseScopeCoordinator(ctx)
	scopeEvents := coordinator.SubscribeEvents(context.Background())
	cancel()
	select {
	case _, open := <-scopeEvents:
		if open {
			t.Fatal("scope event stream remained open after coordinator context ended")
		}
	case <-time.After(time.Second):
		t.Fatal("scope event stream did not close with coordinator context")
	}
}

func TestExecutorEndResponseScopeReturnsDeferredWithoutCallingHandler(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	registry := NewRegistry()
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "report", InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"sent":true}`), nil
		},
		Trigger: EndResponseScope,
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1, Config{ResponseScopes: coordinator})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.execute(context.Background(), scopeToolRequest("turn", `{"message":"hello"}`))
	if result.Result.Status != agentruntime.ToolResultSucceeded || result.TurnBehavior != agentruntime.ToolTurnContinue {
		t.Fatalf("result = %+v, want successful continuing deferral", result)
	}
	assertDeferredScopeResult(t, result.Result.Output, 0, 1, "scheduled")
	if calls != 0 {
		t.Fatalf("handler calls = %d before scope end, want zero", calls)
	}
	coordinator.FinishTurn("session", "turn")
	if calls != 1 {
		t.Fatalf("handler calls = %d after scope end, want one", calls)
	}
}

func scopeToolRequest(turnID, arguments string) agentruntime.ToolRequest {
	return agentruntime.ToolRequest{
		SessionID: "session",
		TurnID:    turnID,
		Call: agentruntime.ToolCall{
			CallID:    "call-" + turnID,
			Name:      "report",
			Arguments: json.RawMessage(arguments),
		},
	}
}

func assertDeferredScopeResult(t *testing.T, raw json.RawMessage, pending, active int, candidate string) {
	t.Helper()
	var result struct {
		Status             string `json:"status"`
		Reason             string `json:"reason"`
		Delivery           string `json:"delivery"`
		Candidate          string `json:"candidate"`
		ActiveTurns        int    `json:"active_turns"`
		PendingCallbacks   int    `json:"pending_callbacks"`
		RetryInCurrentTurn bool   `json:"retry_in_current_turn"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result %s: %v", raw, err)
	}
	if result.Status != "deferred" || result.Reason != "response_scope_active" ||
		result.Delivery != "end_response_scope" || result.Candidate != candidate ||
		result.ActiveTurns != active || result.PendingCallbacks != pending || result.RetryInCurrentTurn {
		t.Fatalf("deferred result = %+v", result)
	}
}

func snapshotStrings(mu *sync.Mutex, values []string) []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), values...)
}
