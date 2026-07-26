package toolexecution

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

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
	rootOutput, err := coordinator.StageAfterResponseScope(context.Background(), scopeToolRequest("root-turn", `{"message":"early"}`), handler)
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
	callbackOutput, err := coordinator.StageAfterResponseScope(context.Background(), scopeToolRequest("callback-turn", `{"message":"final"}`), handler)
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
		t.Fatal("late callback replay reopened a finalized response scope")
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
		t.Fatal("late replay from finalized scope consumed newer response-scope work")
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
	if _, err := coordinator.StageAfterResponseScope(context.Background(), scopeToolRequest("callback-1", `{"message":"waiting"}`), handler); err != nil {
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
	if _, err := coordinator.StageAfterResponseScope(context.Background(), scopeToolRequest("callback-replay", `{"message":"replay"}`), handler); err != nil {
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
	if _, err := coordinator.StageAfterResponseScope(context.Background(), scopeToolRequest("callback-2", `{"message":"done after failed or incomplete callback"}`), handler); err != nil {
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

func TestExecutorAfterResponseScopeReturnsDeferredWithoutCallingHandler(t *testing.T) {
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
		Lifecycle: AfterResponseScope,
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1, Config{ResponseScopes: coordinator})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.execute(context.Background(), scopeToolRequest("turn", `{"message":"hello"}`))
	if result.Result.Status != agentruntime.ToolResultSucceeded || result.TurnBehavior != EndTurn {
		t.Fatalf("result = %+v, want successful end-turn deferral", result)
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
		result.Delivery != "after_response_scope" || result.Candidate != candidate ||
		result.ActiveTurns != active || result.PendingCallbacks != pending || result.RetryInCurrentTurn {
		t.Fatalf("deferred result = %+v", result)
	}
}

func snapshotStrings(mu *sync.Mutex, values []string) []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), values...)
}
