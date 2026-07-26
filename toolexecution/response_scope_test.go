package toolexecution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestResponseScopeRecordsCanonicalAssistantAfterSuccessfulFinalExecution(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	var recorded []agentruntime.Message
	coordinator.SetCanonicalAssistantRecorder(func(_ context.Context, message agentruntime.Message) error {
		recorded = append(recorded, message)
		return nil
	})
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	handler := func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		if len(recorded) != 0 {
			t.Fatal("canonical assistant message recorded before delivery completed")
		}
		return json.RawMessage(`{"status":"reported"}`), nil
	}
	request := scopeToolRequest("turn", `{"message":"Hello from Discord"}`)
	request.CompletionBoundary = true
	if _, executed, err := coordinator.ExecuteEndResponseScope(
		context.Background(),
		request,
		handler,
		"message",
	); err != nil {
		t.Fatal(err)
	} else if !executed {
		t.Fatal("final EndResponseScope call was skipped")
	}
	if len(recorded) != 0 {
		t.Fatalf("canonical messages before turn transcript completed = %#v", recorded)
	}
	coordinator.FinishTurn("session", "turn")
	if len(recorded) != 1 {
		t.Fatalf("canonical messages = %#v, want one", recorded)
	}
	if got := recorded[0]; got.SessionID != "session" ||
		got.TurnID != "turn" ||
		got.Type != agentruntime.MessageTypeAssistant ||
		got.Content != "Hello from Discord" {
		t.Fatalf("canonical assistant message = %#v", got)
	}
}

func TestResponseScopeDoesNotRecordCanonicalAssistantWhenDeliveryFails(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	recorded := 0
	coordinator.SetCanonicalAssistantRecorder(func(context.Context, agentruntime.Message) error {
		recorded++
		return nil
	})
	if err := coordinator.BeginRootTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	request := scopeToolRequest("turn", `{"message":"not delivered"}`)
	request.CompletionBoundary = true
	if _, executed, err := coordinator.ExecuteEndResponseScope(
		context.Background(),
		request,
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("discord unavailable")
		},
		"message",
	); err == nil {
		t.Fatal("failed final delivery returned nil error")
	} else if !executed {
		t.Fatal("failed final delivery was not attempted")
	}

	coordinator.FinishTurn("session", "turn")
	if recorded != 0 {
		t.Fatalf("canonical messages = %d, want none after failed delivery", recorded)
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
		t.Fatalf("result = %+v, want successful skipped call that continues", result)
	}
	assertSkippedScopeResult(t, result.Result.Output)
	if result.Result.TriggerSatisfied == nil || *result.Result.TriggerSatisfied {
		t.Fatalf("early trigger satisfaction = %v, want false", result.Result.TriggerSatisfied)
	}
	if calls != 0 {
		t.Fatalf("handler calls = %d after early call, want zero", calls)
	}

	finalRequest := scopeToolRequest("turn", `{"message":"final"}`)
	finalRequest.CompletionBoundary = true
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
		Executed    bool   `json:"executed"`
		Reason      string `json:"reason"`
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result %s: %v", raw, err)
	}
	if result.Status != "skipped" || result.Executed ||
		result.Reason != "response_scope_not_ready_to_end" ||
		!strings.Contains(result.Instruction, "do not retry") {
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
