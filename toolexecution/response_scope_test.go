package toolexecution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
)

func TestResponseScopeLoggerRecordsLifecycleAndDetails(t *testing.T) {
	var logs bytes.Buffer
	coordinator := NewResponseScopeCoordinator(context.Background())
	coordinator.SetLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.FinishTurn("session", "turn")

	output := logs.String()
	for _, required := range []string{
		`msg="response scope started"`,
		`msg="response scope ending"`,
		`msg="response scope ending details"`,
		`msg="response scope ended"`,
		`msg="response scope ended details"`,
		`session_id=session`,
		`scope_id=turn`,
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("scope logs do not contain %q:\n%s", required, output)
		}
	}
}

func TestResponseScopeSkipsEarlyCallAndExecutesOnlyAtFinalBoundary(t *testing.T) {
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
	rootOutput, executed, err := coordinator.ExecuteEndResponseScope(
		context.Background(),
		scopeToolRequest("root-turn", `{"message":"early"}`),
		handler,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSkippedScopeResult(t, rootOutput)
	if executed {
		t.Fatal("early EndResponseScope call executed")
	}

	coordinator.FinishTurn("session", "root-turn")
	if got := snapshotStrings(&mu, received); len(got) != 0 {
		t.Fatalf("handler calls after root turn = %v, want none", got)
	}

	reservation, err := coordinator.ReserveCallbackTurn("session", "callback-turn", "child", "child-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	reservation.Commit()
	request := scopeToolRequest("callback-turn", `{"message":"final"}`)
	request.CompletionBoundary = true
	callbackOutput, executed, err := coordinator.ExecuteEndResponseScope(
		context.Background(),
		request,
		handler,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !executed || string(callbackOutput) != `{"sent":true}` {
		t.Fatalf("final execution = (%t, %s), want executed handler result", executed, callbackOutput)
	}
	if got := snapshotStrings(&mu, received); len(got) != 1 || got[0] != `{"message":"final"}` {
		t.Fatalf("handler calls = %v, want final call once", got)
	}
	coordinator.FinishTurn("session", "callback-turn")
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

func TestResponseScopeCancelChildDispatchesReleasesPendingCallbackBarrier(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	rollback := coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch-1")
	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch-2")
	if coordinator.ReadyToEnd("session", "root-turn") {
		t.Fatal("scope became ready while child callbacks were pending")
	}

	if cancelled := coordinator.CancelChildDispatches("session", "child"); cancelled != 2 {
		t.Fatalf("cancelled dispatches = %d, want 2", cancelled)
	}
	if !coordinator.ReadyToEnd("session", "root-turn") {
		t.Fatal("scope did not become ready after destructive child close cancelled every callback obligation")
	}
	if cancelled := coordinator.CancelChildDispatches("session", "child"); cancelled != 0 {
		t.Fatalf("second cancellation = %d, want 0", cancelled)
	}
	rollback()
	if !coordinator.ReadyToEnd("session", "root-turn") {
		t.Fatal("late dispatch rollback changed the already-cancelled barrier")
	}
	if _, err := coordinator.ReserveCallbackTurn("session", "callback-turn", "child", "child-turn"); !errors.Is(err, ErrResponseScopeDispatchNotFound) {
		t.Fatalf("callback after destructive close error = %v, want ErrResponseScopeDispatchNotFound", err)
	}
}

func TestResponseScopeCancelChildDispatchesPreventsReservationRollbackFromRestoringObligation(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch-1")
	coordinator.FinishTurn("session", "root-turn")

	reservation, err := coordinator.ReserveCallbackTurn(
		"session",
		"rejected-callback-turn",
		"child",
		"child-turn",
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled := coordinator.CancelChildDispatches("session", "child"); cancelled != 0 {
		t.Fatalf("cancelled queued dispatches = %d, want 0 for reserved callback", cancelled)
	}
	reservation.Rollback("child", "child-turn")

	coordinator.mu.Lock()
	scope := coordinator.scopes[responseScopeKey{sessionID: "session", scopeID: "root-turn"}]
	if scope == nil {
		coordinator.mu.Unlock()
		t.Fatal("response scope disappeared")
	}
	pendingCallbacks := scope.pendingCallbacks
	activeTurns := scope.activeTurns
	coordinator.mu.Unlock()
	if pendingCallbacks != 0 {
		t.Fatalf("reservation rollback restored %d callback obligations after destructive close", pendingCallbacks)
	}
	if activeTurns != 0 {
		t.Fatalf("active turns after rejected callback rollback = %d, want 0", activeTurns)
	}
	if _, err := coordinator.ReserveCallbackTurn(
		"session",
		"retry-callback-turn",
		"child",
		"child-turn",
	); !errors.Is(err, ErrResponseScopeDispatchNotFound) {
		t.Fatalf("callback retry after close error = %v, want ErrResponseScopeDispatchNotFound", err)
	}

	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch-after-close")
	coordinator.mu.Lock()
	pendingCallbacks = scope.pendingCallbacks
	coordinator.mu.Unlock()
	if pendingCallbacks != 0 {
		t.Fatalf("dispatch registration recreated %d obligations for a closed child", pendingCallbacks)
	}
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
	if _, executed, err := executeScopeTool(coordinator, "callback-1", `{"message":"waiting"}`, false, handler); err != nil {
		t.Fatal(err)
	} else if executed {
		t.Fatal("follow-up-pending call executed")
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
	if _, executed, err := executeScopeTool(coordinator, "callback-replay", `{"message":"replay"}`, false, handler); err != nil {
		t.Fatal(err)
	} else if executed {
		t.Fatal("callback replay call executed")
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
	if _, executed, err := executeScopeTool(coordinator, "callback-2", `{"message":"done after failed or incomplete callback"}`, true, handler); err != nil {
		t.Fatal(err)
	} else if !executed {
		t.Fatal("final callback call was skipped")
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

func TestResponseScopeCallbackProgressTracksPendingAndReceivedIdentities(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatchMetadata("session", "root-turn", ResponseScopePendingCallback{
		SubagentID: "child-a", DefinitionName: "web-summary", DisplayName: "Vale",
		DispatchID: "dispatch-a", TurnID: "child-turn-a",
	})
	coordinator.RegisterDispatchMetadata("session", "root-turn", ResponseScopePendingCallback{
		SubagentID: "child-b", DefinitionName: "web-summary", DisplayName: "Luna",
		DispatchID: "dispatch-b",
	})

	first, err := coordinator.ReserveCallbackTurnWithMetadata("session", "callback-a", ResponseScopeReceivedCallback{
		SubagentID: "child-a", DefinitionName: "web-summary", DisplayName: "Vale",
		TurnID: "child-turn-a", OutcomeStatus: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstProgress := first.CallbackProgress()
	if firstProgress.RemainingCallbacks != 1 || firstProgress.AllCallbacksReceived ||
		len(firstProgress.PendingCallbacks) != 1 || len(firstProgress.ReceivedCallbacks) != 1 {
		t.Fatalf("first callback progress = %#v", firstProgress)
	}
	if pending := firstProgress.PendingCallbacks[0]; pending.SubagentID != "child-b" ||
		pending.DefinitionName != "web-summary" || pending.DisplayName != "Luna" ||
		pending.DispatchID != "dispatch-b" || pending.TurnID != "" {
		t.Fatalf("pending callback = %#v", pending)
	}
	if received := firstProgress.ReceivedCallbacks[0]; received.SubagentID != "child-a" ||
		received.TurnID != "child-turn-a" || received.OutcomeStatus != "completed" ||
		received.DispatchID != "dispatch-a" {
		t.Fatalf("received callback = %#v", received)
	}
	first.Commit()

	second, err := coordinator.ReserveCallbackTurnWithMetadata("session", "callback-b", ResponseScopeReceivedCallback{
		SubagentID: "child-b", DefinitionName: "web-summary", DisplayName: "Luna",
		TurnID: "child-turn-b", OutcomeStatus: "failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondProgress := second.CallbackProgress()
	if secondProgress.RemainingCallbacks != 0 || !secondProgress.AllCallbacksReceived ||
		len(secondProgress.PendingCallbacks) != 0 || len(secondProgress.ReceivedCallbacks) != 2 {
		t.Fatalf("second callback progress = %#v", secondProgress)
	}
	if received := secondProgress.ReceivedCallbacks[1]; received.SubagentID != "child-b" ||
		received.TurnID != "child-turn-b" || received.OutcomeStatus != "failed" ||
		received.DispatchID != "dispatch-b" {
		t.Fatalf("second received callback = %#v", received)
	}
	second.Commit()
}

func TestResponseScopeCallbackProgressRollbackRemovesUnacceptedCallback(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatchMetadata("session", "root-turn", ResponseScopePendingCallback{
		SubagentID: "child", DefinitionName: "operator", DisplayName: "Nova",
		DispatchID: "dispatch", TurnID: "child-turn",
	})
	rejected, err := coordinator.ReserveCallbackTurnWithMetadata("session", "rejected", ResponseScopeReceivedCallback{
		SubagentID: "child", DefinitionName: "operator", DisplayName: "Nova",
		TurnID: "child-turn", OutcomeStatus: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rejected.CallbackProgress(); len(got.ReceivedCallbacks) != 1 {
		t.Fatalf("reserved progress = %#v", got)
	}
	rejected.Rollback("child", "child-turn")

	retry, err := coordinator.ReserveCallbackTurnWithMetadata("session", "accepted", ResponseScopeReceivedCallback{
		SubagentID: "child", DefinitionName: "operator", DisplayName: "Nova",
		TurnID: "child-turn", OutcomeStatus: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	progress := retry.CallbackProgress()
	if len(progress.ReceivedCallbacks) != 1 || progress.RemainingCallbacks != 0 ||
		!progress.AllCallbacksReceived {
		t.Fatalf("retried callback progress = %#v", progress)
	}
	retry.Commit()
}

func TestResponseScopeCleanupRunsBeforeFinalHandlerAndSeesTouchedChildren(t *testing.T) {
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
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		events = append(events, "handler")
		return json.RawMessage(`{}`), nil
	}
	coordinator.FinishTurn("session", "root-turn")
	if _, executed, err := executeScopeTool(coordinator, "callback-turn", `{}`, true, handler); err != nil {
		t.Fatal(err)
	} else if !executed {
		t.Fatal("final handler was skipped")
	}
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
	go func() {
		_, _, _ = executeScopeTool(coordinator, "turn", `{}`, true, func(context.Context, json.RawMessage) (json.RawMessage, error) {
			close(handlerCalled)
			return json.RawMessage(`{}`), nil
		})
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

func TestResponseScopeCleanupFailureDoesNotSuppressFinalHandler(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	coordinator.SetCleanup(func(context.Context, string, string, []string) {
		panic("cleanup failed")
	})
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, executed, err := executeScopeTool(coordinator, "turn", `{}`, true, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}); err != nil {
		t.Fatal(err)
	} else if !executed {
		t.Fatal("final handler was skipped")
	}
	coordinator.FinishTurn("session", "turn")
	if calls != 1 {
		t.Fatalf("final handler calls = %d, want one after cleanup failure", calls)
	}
}

func TestResponseScopeEndEventSurvivesCleanupPanic(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	scopeEvents := coordinator.SubscribeEvents(context.Background())
	coordinator.SetCleanup(func(context.Context, string, string, []string) {
		panic("cleanup failed")
	})
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	if _, executed, err := executeScopeTool(coordinator, "turn", `{}`, true, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}); err != nil {
		t.Fatal(err)
	} else if !executed {
		t.Fatal("final handler was skipped")
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

func TestExecutorEndResponseScopeSkipsEarlyCallAndExecutesAtCompletionBoundary(t *testing.T) {
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
		Trigger:          EndResponseScope,
		EndTurnOnSuccess: true,
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1, Config{ResponseScopes: coordinator})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.execute(context.Background(), scopeToolRequest("turn", `{"message":"hello"}`))
	if result.Result.Status != agentruntime.ToolResultSucceeded || result.TurnBehavior != agentruntime.ToolTurnContinue {
		t.Fatalf("result = %+v, want premature skipped call to continue normal work", result)
	}
	assertSkippedScopeResult(t, result.Result.Output)
	if result.Result.TriggerSatisfied == nil || *result.Result.TriggerSatisfied {
		t.Fatalf("early trigger satisfaction = %v, want false", result.Result.TriggerSatisfied)
	}
	if calls != 0 {
		t.Fatalf("handler calls = %d after early call, want zero", calls)
	}

	finalRequest := scopeToolRequest("turn", `{"message":"final"}`)
	finalRequest.ProviderStep = 2
	finalResult := executor.execute(context.Background(), finalRequest)
	if finalResult.Result.Status != agentruntime.ToolResultSucceeded ||
		finalResult.TurnBehavior != agentruntime.ToolTurnEndOnSuccess ||
		finalResult.Result.TriggerSatisfied == nil ||
		!*finalResult.Result.TriggerSatisfied {
		t.Fatalf("final result = %+v, want executed satisfied end-on-success", finalResult)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d after final call, want one", calls)
	}
	coordinator.FinishTurn("session", "turn")
}

func TestExecutorEndResponseScopeDoesNotExecuteMultipleInitialToolCalls(t *testing.T) {
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
		Trigger:          EndResponseScope,
		EndTurnOnSuccess: true,
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1, Config{ResponseScopes: coordinator})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		request := scopeToolRequest("turn", fmt.Sprintf(`{"message":"early-%d"}`, index))
		request.ProviderStep = 1
		result := executor.execute(context.Background(), request)
		if result.Result.TriggerSatisfied == nil || *result.Result.TriggerSatisfied {
			t.Fatalf("initial call %d trigger satisfaction = %v, want false", index, result.Result.TriggerSatisfied)
		}
	}
	if calls != 0 {
		t.Fatalf("handler calls = %d, want zero for every call in the initial provider round", calls)
	}
	coordinator.FinishTurn("session", "turn")
}

func TestExecutorEndResponseScopeCallbackFinalReportExecutesOnFirstProviderRound(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch")
	coordinator.FinishTurn("session", "root-turn")
	callback, err := coordinator.ReserveCallbackTurn("session", "callback-turn", "child", "child-turn")
	if err != nil {
		t.Fatal(err)
	}
	callback.Commit()

	calls := 0
	registry := NewRegistry()
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "report", InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"sent":true}`), nil
		},
		Trigger:          EndResponseScope,
		EndTurnOnSuccess: true,
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1, Config{ResponseScopes: coordinator})
	if err != nil {
		t.Fatal(err)
	}

	initial := scopeToolRequest("callback-turn", `{"message":"premature"}`)
	initial.ProviderStep = 1
	initialResult := executor.execute(context.Background(), initial)
	if initialResult.Result.TriggerSatisfied == nil || !*initialResult.Result.TriggerSatisfied ||
		initialResult.TurnBehavior != agentruntime.ToolTurnEndOnSuccess {
		t.Fatalf("initial callback report = %+v, want executed end-on-success", initialResult)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want one", calls)
	}
	coordinator.FinishTurn("session", "callback-turn")
}

func TestResponseScopeInlineCallbackStaysPendingUntilRuntimeInputCommit(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch")
	reservation, err := coordinator.ReserveInlineCallback("session", "root-turn", "child", "child-turn")
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.ReadyToEnd("session", "root-turn") {
		t.Fatal("scope became ready before inline runtime input was committed")
	}
	reservation.Commit()
	if !coordinator.ReadyToEnd("session", "root-turn") {
		t.Fatal("scope did not become ready after inline runtime input commit")
	}
	coordinator.FinishTurn("session", "root-turn")
}

func TestResponseScopeToolBudgetIsSharedWithCallbackTurns(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterDispatch("session", "root-turn", "child", "dispatch")
	used, allowed, err := coordinator.ReserveToolCall("session", "root-turn", "web_search", 2)
	if err != nil || !allowed || used != 1 {
		t.Fatalf("root reservation = used %d allowed %v err %v", used, allowed, err)
	}
	coordinator.FinishTurn("session", "root-turn")
	callback, err := coordinator.ReserveCallbackTurn("session", "callback-turn", "child", "child-turn")
	if err != nil {
		t.Fatal(err)
	}
	callback.Commit()
	used, allowed, err = coordinator.ReserveToolCall("session", "callback-turn", "web_search", 2)
	if err != nil || !allowed || used != 2 {
		t.Fatalf("callback reservation = used %d allowed %v err %v", used, allowed, err)
	}
	used, allowed, err = coordinator.ReserveToolCall("session", "callback-turn", "web_search", 2)
	if err != nil || allowed || used != 2 {
		t.Fatalf("over-budget reservation = used %d allowed %v err %v", used, allowed, err)
	}
	coordinator.FinishTurn("session", "callback-turn")
}

func TestExecutorReturnsControlledSuccessAfterResponseScopeToolBudget(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	handlerCalls := 0
	registry := NewRegistry()
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "search", InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			handlerCalls++
			return json.RawMessage(`{"results":[]}`), nil
		},
		ResponseScopeCallLimit: 2,
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1, Config{ResponseScopes: coordinator})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan agentruntime.ToolRequest, 3)
	results := make(chan agentruntime.ToolResultEnvelope, 3)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := runExecutor(executor, ctx, requests, results, interrupts)
	for index := 1; index <= 3; index++ {
		requests <- toolRequest("session", "turn", fmt.Sprintf("call-%d", index), "search", `{}`)
	}
	var exhausted agentruntime.ToolResult
	for range 3 {
		result := waitResult(t, results).Result
		if strings.Contains(string(result.Output), "response_scope_tool_budget_exhausted") {
			exhausted = result
		}
	}
	if handlerCalls != 2 {
		t.Fatalf("handler calls = %d, want 2", handlerCalls)
	}
	if exhausted.Status != agentruntime.ToolResultSucceeded || exhausted.Error != "" {
		t.Fatalf("exhausted result = %+v, want controlled success", exhausted)
	}
	cancel()
	waitDone(t, done)
	coordinator.FinishTurn("session", "turn")
}

func TestExecutorMarksBudgetSkippedEndResponseScopeTriggerUnsatisfied(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "report", InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"sent":true}`), nil
		},
		Trigger:                EndResponseScope,
		ResponseScopeCallLimit: 1,
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1, Config{ResponseScopes: coordinator})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan agentruntime.ToolRequest, 2)
	results := make(chan agentruntime.ToolResultEnvelope, 2)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := runExecutor(executor, ctx, requests, results, interrupts)
	requests <- toolRequest("session", "turn", "first", "report", `{}`)
	requests <- toolRequest("session", "turn", "budget-skipped", "report", `{}`)

	var exhausted agentruntime.ToolResult
	for range 2 {
		result := waitResult(t, results).Result
		if strings.Contains(string(result.Output), "response_scope_tool_budget_exhausted") {
			exhausted = result
		}
	}
	if exhausted.Status != agentruntime.ToolResultSucceeded || exhausted.TriggerSatisfied == nil || *exhausted.TriggerSatisfied {
		t.Fatalf("budget-skipped end-response-scope result = %+v, want successful controlled skip with trigger_satisfied=false", exhausted)
	}
	cancel()
	waitDone(t, done)
	coordinator.FinishTurn("session", "turn")
}

func TestExecutorEndResponseScopeSkippedCallEndsTurnWhileCallbackPending(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	rollback := coordinator.RegisterDispatch("session", "turn", "child", "dispatch")
	calls := 0
	registry := NewRegistry()
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "report", InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"sent":true}`), nil
		},
		Trigger:          EndResponseScope,
		EndTurnOnSuccess: true,
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1, Config{ResponseScopes: coordinator})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.execute(context.Background(), scopeToolRequest("turn", `{"message":"waiting"}`))
	if result.Result.Status != agentruntime.ToolResultSucceeded ||
		result.TurnBehavior != agentruntime.ToolTurnEndOnSuccess ||
		result.Result.TriggerSatisfied == nil ||
		*result.Result.TriggerSatisfied {
		t.Fatalf("result = %+v, want skipped unsatisfied call that ends only the callback-pending turn", result)
	}
	if calls != 0 {
		t.Fatalf("handler calls = %d, want zero while callback is pending", calls)
	}
	rollback()
	coordinator.FinishTurn("session", "turn")
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

func assertSkippedScopeResult(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var result struct {
		Status      string `json:"status"`
		Action      string `json:"action"`
		Executed    bool   `json:"executed"`
		Reason      string `json:"reason"`
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result %s: %v", raw, err)
	}
	const instruction = "The tool call was processed successfully, but the tool action was skipped because this end-of-scope tool was called at the wrong time. " +
		"Treat this result as success and do not retry the tool yourself."
	if result.Status != "succeeded" ||
		result.Action != "skipped" ||
		result.Executed ||
		result.Reason != "tool_called_at_wrong_time" ||
		result.Instruction != instruction {
		t.Fatalf("skipped result = %+v", result)
	}
}

func executeScopeTool(
	coordinator *ResponseScopeCoordinator,
	turnID string,
	arguments string,
	completionBoundary bool,
	handler Handler,
) (json.RawMessage, bool, error) {
	request := scopeToolRequest(turnID, arguments)
	request.CompletionBoundary = completionBoundary
	return coordinator.ExecuteEndResponseScope(context.Background(), request, handler)
}

func snapshotStrings(mu *sync.Mutex, values []string) []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), values...)
}
