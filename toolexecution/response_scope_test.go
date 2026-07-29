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
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
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

func TestResponseScopeLimitsFailedRecoveryBySubagentAndFingerprint(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}

	allowed, rollback := coordinator.ReserveFailedRecovery("session", "root-turn", "subagent-1", "context # exceeded #")
	if !allowed {
		t.Fatal("first recovery was rejected")
	}
	if allowed, _ := coordinator.ReserveFailedRecovery("session", "root-turn", "subagent-1", "context # exceeded #"); allowed {
		t.Fatal("duplicate subagent failure recovery was accepted")
	}
	if allowed, _ := coordinator.ReserveFailedRecovery("session", "root-turn", "subagent-1", "provider unavailable"); !allowed {
		t.Fatal("different failure fingerprint was rejected")
	}
	if allowed, _ := coordinator.ReserveFailedRecovery("session", "root-turn", "subagent-2", "context # exceeded #"); !allowed {
		t.Fatal("same failure on another subagent was rejected")
	}

	rollback()
	if allowed, _ := coordinator.ReserveFailedRecovery("session", "root-turn", "subagent-1", "context # exceeded #"); !allowed {
		t.Fatal("rolled-back recovery reservation remained exhausted")
	}
}

func TestResponseScopeSkipsEarlyCallAndExecutesOnlyAtFinalBoundary(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment-1")

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
		t.Fatalf("handler calls after main-agent turn = %v, want none", got)
	}

	reservation, err := coordinator.ReserveResultTurn("session", "result-turn", "subagent", "subagent-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	reservation.Commit()
	request := scopeToolRequest("result-turn", `{"message":"final"}`)
	request.CompletionBoundary = true
	resultOutput, executed, err := coordinator.ExecuteEndResponseScope(
		context.Background(),
		request,
		handler,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !executed || string(resultOutput) != `{"sent":true}` {
		t.Fatalf("final execution = (%t, %s), want executed handler result", executed, resultOutput)
	}
	if got := snapshotStrings(&mu, received); len(got) != 1 || got[0] != `{"message":"final"}` {
		t.Fatalf("handler calls = %v, want final call once", got)
	}
	coordinator.FinishTurn("session", "result-turn")
	if _, err := coordinator.ReserveResultTurn("session", "late-replay", "subagent", "subagent-turn-1"); err == nil {
		t.Fatal("late result replay reopened an ended response scope")
	}
	if got := snapshotStrings(&mu, received); len(got) != 1 {
		t.Fatalf("handler calls after late replay = %v, want exactly one", got)
	}

	if err := coordinator.BeginMainAgentTurn("session", "new-root"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignment("session", "new-root", "subagent", "new-assignment")
	coordinator.FinishTurn("session", "new-root")
	if _, err := coordinator.ReserveResultTurn("session", "late-replay-with-new-work", "subagent", "subagent-turn-1"); err == nil {
		t.Fatal("late replay from ended scope consumed newer response-scope work")
	}
	newResult, err := coordinator.ReserveResultTurn("session", "new-result", "subagent", "subagent-turn-2")
	if err != nil {
		t.Fatalf("new result after late replay error = %v", err)
	}
	newResult.Commit()
}

func TestResponseScopeCancelSubagentAssignmentsReleasesPendingResultBarrier(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	rollback := coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment-1")
	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment-2")
	if coordinator.ReadyToEnd("session", "root-turn") {
		t.Fatal("scope became ready while subagent results were pending")
	}

	if cancelled := coordinator.CancelSubagentAssignments("session", "subagent"); cancelled != 2 {
		t.Fatalf("cancelled assignments = %d, want 2", cancelled)
	}
	if !coordinator.ReadyToEnd("session", "root-turn") {
		t.Fatal("scope did not become ready after destructive subagent close cancelled every result obligation")
	}
	if cancelled := coordinator.CancelSubagentAssignments("session", "subagent"); cancelled != 0 {
		t.Fatalf("second cancellation = %d, want 0", cancelled)
	}
	rollback()
	if !coordinator.ReadyToEnd("session", "root-turn") {
		t.Fatal("late assignment rollback changed the already-cancelled barrier")
	}
	if _, err := coordinator.ReserveResultTurn("session", "result-turn", "subagent", "subagent-turn"); !errors.Is(err, ErrResponseScopeAssignmentNotFound) {
		t.Fatalf("result after destructive close error = %v, want ErrResponseScopeAssignmentNotFound", err)
	}
}

func TestResponseScopeCancelSubagentAssignmentsPreventsReservationRollbackFromRestoringObligation(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment-1")
	coordinator.FinishTurn("session", "root-turn")

	reservation, err := coordinator.ReserveResultTurn(
		"session",
		"rejected-result-turn",
		"subagent",
		"subagent-turn",
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled := coordinator.CancelSubagentAssignments("session", "subagent"); cancelled != 0 {
		t.Fatalf("cancelled queued assignments = %d, want 0 for reserved result", cancelled)
	}
	reservation.Rollback("subagent", "subagent-turn")

	coordinator.mu.Lock()
	scope := coordinator.scopes[responseScopeKey{sessionID: "session", scopeID: "root-turn"}]
	if scope == nil {
		coordinator.mu.Unlock()
		t.Fatal("response scope disappeared")
	}
	pendingResults := scope.pendingResults
	activeTurns := scope.activeTurns
	coordinator.mu.Unlock()
	if pendingResults != 0 {
		t.Fatalf("reservation rollback restored %d result obligations after destructive close", pendingResults)
	}
	if activeTurns != 0 {
		t.Fatalf("active turns after rejected result rollback = %d, want 0", activeTurns)
	}
	if _, err := coordinator.ReserveResultTurn(
		"session",
		"retry-result-turn",
		"subagent",
		"subagent-turn",
	); !errors.Is(err, ErrResponseScopeAssignmentNotFound) {
		t.Fatalf("result retry after close error = %v, want ErrResponseScopeAssignmentNotFound", err)
	}

	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment-after-close")
	coordinator.mu.Lock()
	pendingResults = scope.pendingResults
	coordinator.mu.Unlock()
	if pendingResults != 0 {
		t.Fatalf("assignment registration recreated %d obligations for a closed subagent", pendingResults)
	}
}

func TestResponseScopeFollowUpReopensBarrierAndResultReplayDoesNotCloseIt(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment-1")
	coordinator.FinishTurn("session", "root-turn")

	first, err := coordinator.ReserveResultTurn("session", "result-1", "subagent", "subagent-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	first.Commit()
	coordinator.RegisterAssignment("session", "result-1", "subagent", "follow-up")

	calls := 0
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}
	if _, executed, err := executeScopeTool(coordinator, "result-1", `{"message":"waiting"}`, false, handler); err != nil {
		t.Fatal(err)
	} else if executed {
		t.Fatal("follow-up-pending call executed")
	}
	coordinator.FinishTurn("session", "result-1")
	if calls != 0 {
		t.Fatalf("handler calls = %d, want none with accepted follow-up pending", calls)
	}

	replay, err := coordinator.ReserveResultTurn("session", "result-replay", "subagent", "subagent-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	replay.Commit()
	if _, executed, err := executeScopeTool(coordinator, "result-replay", `{"message":"replay"}`, false, handler); err != nil {
		t.Fatal(err)
	} else if executed {
		t.Fatal("result replay call executed")
	}
	coordinator.FinishTurn("session", "result-replay")
	if calls != 0 {
		t.Fatalf("handler calls = %d, replay closed a newer pending assignment", calls)
	}

	second, err := coordinator.ReserveResultTurn("session", "result-2", "subagent", "subagent-turn-2")
	if err != nil {
		t.Fatal(err)
	}
	second.Commit()
	if _, executed, err := executeScopeTool(coordinator, "result-2", `{"message":"done after failed or incomplete result"}`, true, handler); err != nil {
		t.Fatal(err)
	} else if !executed {
		t.Fatal("final result call was skipped")
	}
	coordinator.FinishTurn("session", "result-2")
	if calls != 1 {
		t.Fatalf("handler calls = %d, want exactly one after terminal result", calls)
	}
}

func TestResponseScopeResultReservationRollbackRestoresPendingAssignment(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment-1")
	coordinator.FinishTurn("session", "root-turn")

	reservation, err := coordinator.ReserveResultTurn("session", "rejected-turn", "subagent", "subagent-turn")
	if err != nil {
		t.Fatal(err)
	}
	reservation.Rollback("subagent", "subagent-turn")

	retry, err := coordinator.ReserveResultTurn("session", "accepted-turn", "subagent", "subagent-turn")
	if err != nil {
		t.Fatalf("ReserveResultTurn() after rollback error = %v", err)
	}
	retry.Commit()
}

func TestResponseScopeResultProgressTracksPendingAndReceivedIdentities(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignmentMetadata("session", "root-turn", ResponseScopePendingResult{
		SubagentID: "subagent-a", DefinitionName: "web-summary", DisplayName: "Vale",
		AssignmentID: "assignment-a", SubagentTurnID: "subagent-turn-a",
	})
	coordinator.RegisterAssignmentMetadata("session", "root-turn", ResponseScopePendingResult{
		SubagentID: "subagent-b", DefinitionName: "web-summary", DisplayName: "Luna",
		AssignmentID: "assignment-b",
	})

	first, err := coordinator.ReserveResultTurnWithMetadata("session", "result-a", ResponseScopeDeliveredResult{
		SubagentID: "subagent-a", DefinitionName: "web-summary", DisplayName: "Vale",
		SubagentTurnID: "subagent-turn-a", ResultStatus: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstProgress := first.ResultProgress()
	if firstProgress.PendingCount != 1 || firstProgress.AllResultsDelivered ||
		len(firstProgress.PendingResults) != 1 || len(firstProgress.DeliveredResults) != 1 {
		t.Fatalf("first result progress = %#v", firstProgress)
	}
	if pending := firstProgress.PendingResults[0]; pending.SubagentID != "subagent-b" ||
		pending.DefinitionName != "web-summary" || pending.DisplayName != "Luna" ||
		pending.AssignmentID != "assignment-b" || pending.SubagentTurnID != "" {
		t.Fatalf("pending result = %#v", pending)
	}
	if received := firstProgress.DeliveredResults[0]; received.SubagentID != "subagent-a" ||
		received.SubagentTurnID != "subagent-turn-a" || received.ResultStatus != "completed" ||
		received.AssignmentID != "assignment-a" {
		t.Fatalf("received result = %#v", received)
	}
	first.Commit()

	second, err := coordinator.ReserveResultTurnWithMetadata("session", "result-b", ResponseScopeDeliveredResult{
		SubagentID: "subagent-b", DefinitionName: "web-summary", DisplayName: "Luna",
		SubagentTurnID: "subagent-turn-b", ResultStatus: "failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondProgress := second.ResultProgress()
	if secondProgress.PendingCount != 0 || !secondProgress.AllResultsDelivered ||
		len(secondProgress.PendingResults) != 0 || len(secondProgress.DeliveredResults) != 2 {
		t.Fatalf("second result progress = %#v", secondProgress)
	}
	if received := secondProgress.DeliveredResults[1]; received.SubagentID != "subagent-b" ||
		received.SubagentTurnID != "subagent-turn-b" || received.ResultStatus != "failed" ||
		received.AssignmentID != "assignment-b" {
		t.Fatalf("second received result = %#v", received)
	}
	second.Commit()
}

func TestResponseScopeResultProgressRollbackRemovesUnacceptedResult(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignmentMetadata("session", "root-turn", ResponseScopePendingResult{
		SubagentID: "subagent", DefinitionName: "operator", DisplayName: "Nova",
		AssignmentID: "assignment", SubagentTurnID: "subagent-turn",
	})
	rejected, err := coordinator.ReserveResultTurnWithMetadata("session", "rejected", ResponseScopeDeliveredResult{
		SubagentID: "subagent", DefinitionName: "operator", DisplayName: "Nova",
		SubagentTurnID: "subagent-turn", ResultStatus: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rejected.ResultProgress(); len(got.DeliveredResults) != 1 {
		t.Fatalf("reserved progress = %#v", got)
	}
	rejected.Rollback("subagent", "subagent-turn")

	retry, err := coordinator.ReserveResultTurnWithMetadata("session", "accepted", ResponseScopeDeliveredResult{
		SubagentID: "subagent", DefinitionName: "operator", DisplayName: "Nova",
		SubagentTurnID: "subagent-turn", ResultStatus: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	progress := retry.ResultProgress()
	if len(progress.DeliveredResults) != 1 || progress.PendingCount != 0 ||
		!progress.AllResultsDelivered {
		t.Fatalf("retried result progress = %#v", progress)
	}
	retry.Commit()
}

func TestResponseScopeCleanupRunsBeforeFinalHandlerAndSeesTouchedSubagents(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	scopeEvents := coordinator.SubscribeEvents(context.Background())
	var events []string
	coordinator.SetCleanup(func(_ context.Context, sessionID, scopeID string, subagentIDs []string) {
		events = append(events, "cleanup:"+sessionID+":"+scopeID+":"+strings.Join(subagentIDs, ","))
	})
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment")
	result, err := coordinator.ReserveResultTurn("session", "result-turn", "subagent", "subagent-turn")
	if err != nil {
		t.Fatal(err)
	}
	result.Commit()
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		events = append(events, "handler")
		return json.RawMessage(`{}`), nil
	}
	coordinator.FinishTurn("session", "root-turn")
	if _, executed, err := executeScopeTool(coordinator, "result-turn", `{}`, true, handler); err != nil {
		t.Fatal(err)
	} else if !executed {
		t.Fatal("final handler was skipped")
	}
	coordinator.FinishTurn("session", "result-turn")
	if got, want := strings.Join(events, "|"), "cleanup:session:root-turn:subagent|handler"; got != want {
		t.Fatalf("scope end order = %q, want %q", got, want)
	}
	preEnd := <-scopeEvents
	end := <-scopeEvents
	if preEnd.Type != PreEndScope || end.Type != EndScope {
		t.Fatalf("scope event types = %q, %q; want %q, %q", preEnd.Type, end.Type, PreEndScope, EndScope)
	}
	for _, event := range []ScopeEvent{preEnd, end} {
		if event.SessionID != "session" || event.ScopeID != "root-turn" || event.TriggerTurnID != "result-turn" {
			t.Fatalf("scope event correlation = %+v", event)
		}
		if got := strings.Join(event.SubagentIDs, ","); got != "subagent" {
			t.Fatalf("scope event subagentIDs = %q, want subagent", got)
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
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
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

func TestResponseScopeCleanupFailureDoesNotSuppressFinalHandler(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	coordinator.SetCleanup(func(context.Context, string, string, []string) {
		panic("cleanup failed")
	})
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
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
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
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

func TestResponseScopeRolledBackAssignmentIsNotCleanupCandidate(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	var subagentIDs []string
	coordinator.SetCleanup(func(_ context.Context, _, _ string, ids []string) {
		subagentIDs = append(subagentIDs, ids...)
	})
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	rollback := coordinator.RegisterAssignment("session", "turn", "subagent", "assignment")
	rollback()
	coordinator.FinishTurn("session", "turn")
	if len(subagentIDs) != 0 {
		t.Fatalf("rolled-back assignment cleanup candidates = %v", subagentIDs)
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
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
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
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
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

func TestExecutorEndResponseScopeResultFinalReportExecutesOnFirstProviderRound(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment")
	coordinator.FinishTurn("session", "root-turn")
	result, err := coordinator.ReserveResultTurn("session", "result-turn", "subagent", "subagent-turn")
	if err != nil {
		t.Fatal(err)
	}
	result.Commit()

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

	initial := scopeToolRequest("result-turn", `{"message":"premature"}`)
	initial.ProviderStep = 1
	initialResult := executor.execute(context.Background(), initial)
	if initialResult.Result.TriggerSatisfied == nil || !*initialResult.Result.TriggerSatisfied ||
		initialResult.TurnBehavior != agentruntime.ToolTurnEndOnSuccess {
		t.Fatalf("initial result report = %+v, want executed end-on-success", initialResult)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want one", calls)
	}
	coordinator.FinishTurn("session", "result-turn")
}

func TestResponseScopeInlineResultStaysPendingUntilRuntimeInputCommit(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment")
	reservation, err := coordinator.ReserveInlineResult("session", "root-turn", "subagent", "subagent-turn")
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

func TestResponseScopeToolBudgetIsSharedWithResultTurns(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "root-turn"); err != nil {
		t.Fatal(err)
	}
	coordinator.RegisterAssignment("session", "root-turn", "subagent", "assignment")
	used, allowed, err := coordinator.ReserveToolCall("session", "root-turn", "web_search", 2)
	if err != nil || !allowed || used != 1 {
		t.Fatalf("root reservation = used %d allowed %v err %v", used, allowed, err)
	}
	coordinator.FinishTurn("session", "root-turn")
	result, err := coordinator.ReserveResultTurn("session", "result-turn", "subagent", "subagent-turn")
	if err != nil {
		t.Fatal(err)
	}
	result.Commit()
	used, allowed, err = coordinator.ReserveToolCall("session", "result-turn", "web_search", 2)
	if err != nil || !allowed || used != 2 {
		t.Fatalf("result reservation = used %d allowed %v err %v", used, allowed, err)
	}
	used, allowed, err = coordinator.ReserveToolCall("session", "result-turn", "web_search", 2)
	if err != nil || allowed || used != 2 {
		t.Fatalf("over-budget reservation = used %d allowed %v err %v", used, allowed, err)
	}
	coordinator.FinishTurn("session", "result-turn")
}

func TestExecutorReturnsControlledSuccessAfterResponseScopeToolBudget(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
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
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
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

func TestExecutorEndResponseScopeSkippedCallEndsTurnWhileResultPending(t *testing.T) {
	coordinator := NewResponseScopeCoordinator(context.Background())
	if err := coordinator.BeginMainAgentTurn("session", "turn"); err != nil {
		t.Fatal(err)
	}
	rollback := coordinator.RegisterAssignment("session", "turn", "subagent", "assignment")
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
		t.Fatalf("result = %+v, want skipped unsatisfied call that ends only the result-pending turn", result)
	}
	if calls != 0 {
		t.Fatalf("handler calls = %d, want zero while result is pending", calls)
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
	const instruction = "The tool call was handled, but the action did not run because the complete final response is not ready. Treat this call as handled and do not retry it yourself. Finish remaining independent work, or stop if required subagent results are still pending."
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
