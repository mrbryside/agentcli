package agentcli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestSubagentManagerStartIsAsyncAndSerializesMailbox(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 2)
	defer manager.Close()

	started := make(chan storage.Subagent, 1)
	errs := make(chan error, 1)
	go func() {
		record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "first", "label")
		if err != nil {
			errs <- err
			return
		}
		started <- record
	}()

	var record storage.Subagent
	select {
	case err := <-errs:
		t.Fatal(err)
	case record = <-started:
	case <-time.After(time.Second):
		t.Fatal("Start waited for subagent completion")
	}
	if record.Status != storage.SubagentStatusRunning || record.SubagentSessionID == "mainAgent" || record.CurrentSubagentTurnID == "" {
		t.Fatalf("start record = %#v", record)
	}
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}

	queued, err := manager.Send(context.Background(), "mainAgent", record.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Pending) != 1 || queued.Pending[0].Content != "second" {
		t.Fatalf("queued record = %#v", queued)
	}

	model.releases <- struct{}{}
	if err := model.waitStarts(2); err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)

	requests := model.Requests()
	if len(requests) != 2 || requests[0].Messages[len(requests[0].Messages)-1].Content != "first" || requests[1].Messages[len(requests[1].Messages)-1].Content != "second" {
		t.Fatalf("subagent requests = %#v", requests)
	}
}

func TestSubagentManagerExecuteTaskWaitsForFinalAssistantResponse(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 1)}
	manager := newTestSubagentManager(t, model, 2)
	defer manager.Close()

	results := make(chan TaskResult, 1)
	errs := make(chan error, 1)
	go func() {
		result, err := manager.ExecuteTask(context.Background(), TaskRequest{
			MainAgentSessionID: "mainAgent", MainAgentTurnID: "main-turn",
			AgentName: "researcher", Description: "Find the answer", Prompt: "research this",
		})
		if err != nil {
			errs <- err
			return
		}
		results <- result
	}()
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-results:
		t.Fatalf("foreground task returned before child completion: %#v", result)
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(25 * time.Millisecond):
	}
	model.releases <- struct{}{}
	select {
	case err := <-errs:
		t.Fatal(err)
	case result := <-results:
		if result.TaskID == "" || result.AgentName != "researcher" || result.State != TaskStateCompleted || result.Output != "done" || result.Error != "" {
			t.Fatalf("task result = %#v", result)
		}
		record, found, err := manager.store.Get(context.Background(), result.TaskID)
		if err != nil || !found {
			t.Fatalf("task record = (%#v, %v, %v)", record, found, err)
		}
		if record.Status != storage.SubagentStatusIdle || record.ActiveTaskDelivery != nil {
			t.Fatalf("foreground task persisted delivery/state = %#v", record)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground task did not return after child completion")
	}
}

func TestSubagentManagerExecuteTaskRunsIndependentlyAcrossManagers(t *testing.T) {
	firstModel := &subagentGateModel{releases: make(chan struct{}, 1)}
	secondModel := &subagentGateModel{releases: make(chan struct{}, 1)}
	firstManager := newTestSubagentManager(t, firstModel, 1)
	secondManager := newTestSubagentManager(t, secondModel, 1)
	defer firstManager.Close()
	defer secondManager.Close()

	type taskOutcome struct {
		result TaskResult
		err    error
	}
	firstDone := make(chan taskOutcome, 1)
	secondDone := make(chan taskOutcome, 1)
	runTask := func(manager *subagentManager, session, turn string, done chan<- taskOutcome) {
		result, err := manager.ExecuteTask(context.Background(), TaskRequest{
			MainAgentSessionID: session, MainAgentTurnID: turn, AgentName: "researcher",
			Description: "Independent work", Prompt: "wait for the gate",
		})
		done <- taskOutcome{result: result, err: err}
	}
	go runTask(firstManager, "first-session", "first-turn", firstDone)
	go runTask(secondManager, "second-session", "second-turn", secondDone)
	if err := firstModel.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	if err := secondModel.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-firstDone:
		t.Fatalf("first manager returned before its gate opened: %#v", outcome)
	case outcome := <-secondDone:
		t.Fatalf("second manager returned before its gate opened: %#v", outcome)
	case <-time.After(25 * time.Millisecond):
	}

	firstModel.releases <- struct{}{}
	var first taskOutcome
	select {
	case first = <-firstDone:
		if first.err != nil || first.result.State != TaskStateCompleted {
			t.Fatalf("first task outcome = %#v", first)
		}
	case outcome := <-secondDone:
		t.Fatalf("second manager was released by first manager's gate: %#v", outcome)
	case <-time.After(time.Second):
		t.Fatal("first manager did not finish after its gate opened")
	}
	firstRecord, found, err := firstManager.store.Get(context.Background(), first.result.TaskID)
	if err != nil || !found {
		t.Fatalf("first record = (%#v, %v, %v)", firstRecord, found, err)
	}
	secondRecords, err := secondManager.List(context.Background(), "second-session", true)
	if err != nil || len(secondRecords) != 1 {
		t.Fatalf("second manager records = (%#v, %v)", secondRecords, err)
	}
	if firstRecord.ActiveTaskDelivery != nil || len(firstRecord.Pending) != 0 || secondRecords[0].ActiveTaskDelivery != nil || len(secondRecords[0].Pending) != 0 || secondRecords[0].Status != storage.SubagentStatusRunning {
		t.Fatalf("foreground managers retained delivery or mailbox state: first=%#v second=%#v", firstRecord, secondRecords[0])
	}

	secondModel.releases <- struct{}{}
	select {
	case second := <-secondDone:
		if second.err != nil || second.result.State != TaskStateCompleted {
			t.Fatalf("second task outcome = %#v", second)
		}
	case <-time.After(time.Second):
		t.Fatal("second manager did not finish after its own gate opened")
	}
}

func TestSubagentManagerExecuteTaskHandlesInstantChildCompletion(t *testing.T) {
	// scriptedModel publishes its terminal stream synchronously. Repeating this
	// exercises the handoff where monitor can clear instance.run before
	// startForegroundTask reads the retained run by turn ID.
	manager := newTestSubagentManager(t, &scriptedModel{}, 32)
	defer manager.Close()
	for index := 0; index < 16; index++ {
		result, err := manager.ExecuteTask(context.Background(), TaskRequest{
			MainAgentSessionID: "mainAgent", MainAgentTurnID: fmt.Sprintf("turn-%d", index),
			AgentName: "researcher", Description: "Instant work", Prompt: "finish immediately",
		})
		if err != nil {
			t.Fatalf("instant task %d returned error: %v", index, err)
		}
		if result.State != TaskStateCompleted || result.Output != "done" {
			t.Fatalf("instant task %d result = %#v", index, result)
		}
	}
}

func TestSubagentManagerExecuteTaskResumesOnlyOwnedIdleTask(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 2)}
	manager := newTestSubagentManager(t, model, 2)
	defer manager.Close()

	firstResults := make(chan TaskResult, 1)
	go func() {
		result, err := manager.ExecuteTask(context.Background(), TaskRequest{
			MainAgentSessionID: "owner", MainAgentTurnID: "first", AgentName: "researcher",
			Description: "Initial research", Prompt: "first question",
		})
		if err != nil {
			t.Errorf("first task: %v", err)
			return
		}
		firstResults <- result
	}()
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	var first TaskResult
	select {
	case first = <-firstResults:
	case <-time.After(time.Second):
		t.Fatal("first task did not finish")
	}
	if _, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "other", MainAgentTurnID: "other-turn", TaskID: first.TaskID, Prompt: "continue",
	}); !errors.Is(err, storage.ErrSubagentNotFound) {
		t.Fatalf("cross-session resume error = %v", err)
	}
	if _, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "owner", MainAgentTurnID: "bad-turn", TaskID: first.TaskID, AgentName: "reviewer", Prompt: "continue",
	}); err == nil {
		t.Fatal("resume accepted a new agent")
	}

	resumedResults := make(chan TaskResult, 1)
	go func() {
		result, err := manager.ExecuteTask(context.Background(), TaskRequest{
			MainAgentSessionID: "owner", MainAgentTurnID: "second", TaskID: first.TaskID, Prompt: "continue",
		})
		if err != nil {
			t.Errorf("resume task: %v", err)
			return
		}
		resumedResults <- result
	}()
	if err := model.waitStarts(2); err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	select {
	case resumed := <-resumedResults:
		if resumed.TaskID != first.TaskID || resumed.State != TaskStateCompleted || resumed.Output != "done" {
			t.Fatalf("resumed result = %#v", resumed)
		}
		record, found, err := manager.store.Get(context.Background(), first.TaskID)
		if err != nil || !found {
			t.Fatalf("resumed record = (%#v, %v, %v)", record, found, err)
		}
		transcript, err := manager.mainAgent.ListMessages(context.Background(), record.SubagentSessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(transcript) < 4 {
			t.Fatalf("resume did not retain task transcript: %#v", transcript)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed task did not finish")
	}
}

func TestSubagentManagerExecuteTaskCancellationInterruptsChild(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan TaskResult, 1)
	go func() {
		result, err := manager.ExecuteTask(ctx, TaskRequest{
			MainAgentSessionID: "mainAgent", MainAgentTurnID: "main-turn", AgentName: "researcher",
			Description: "Long work", Prompt: "wait",
		})
		if err != nil {
			t.Errorf("execute task: %v", err)
			return
		}
		results <- result
	}()
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case result := <-results:
		if result.State != TaskStateError || !strings.Contains(result.Error, context.Canceled.Error()) {
			t.Fatalf("cancelled task result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled task did not return")
	}
}

func TestSubagentManagerExecuteTaskRejectsUnknownRunningAndClosedTaskIDs(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "mainAgent", "origin", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []TaskRequest{
		{MainAgentSessionID: "mainAgent", MainAgentTurnID: "resume", TaskID: "unknown", Prompt: "continue"},
		{MainAgentSessionID: "mainAgent", MainAgentTurnID: "resume", TaskID: record.ID, Prompt: "continue"},
	} {
		_, err := manager.ExecuteTask(context.Background(), request)
		if request.TaskID == "unknown" {
			if !errors.Is(err, storage.ErrSubagentNotFound) {
				t.Fatalf("unknown task resume error = %v", err)
			}
		} else if !errors.Is(err, storage.ErrSubagentRunning) {
			t.Fatalf("running task resume error = %v", err)
		}
	}
	if _, err := manager.CloseSubagent(context.Background(), "mainAgent", record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "mainAgent", MainAgentTurnID: "resume", TaskID: record.ID, Prompt: "continue",
	}); !errors.Is(err, storage.ErrSubagentClosed) {
		t.Fatalf("closed task resume error = %v", err)
	}
}

func TestSubagentManagerDeduplicatesMainAgentTurnMessages(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 2)
	defer manager.Close()

	record, err := manager.Start(context.Background(), "mainAgent", "turn-1", "researcher", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	exact, err := manager.SendFromMainAgentTurn(context.Background(), "mainAgent", "turn-1", record.ID, " first \r\n")
	if err != nil {
		t.Fatal(err)
	}
	if exact.Action != toolexecution.SubagentSendDuplicate || exact.Accepted || !exact.Deduplicated || len(exact.Subagent.Pending) != 0 || len(exact.IdempotencyKey) != 64 {
		t.Fatalf("exact duplicate = %#v", exact)
	}
	changed, err := manager.SendFromMainAgentTurn(context.Background(), "mainAgent", "turn-1", record.ID, "different wording")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Action != toolexecution.SubagentSendAlreadySent || changed.Accepted || changed.Deduplicated || len(changed.Subagent.Pending) != 0 {
		t.Fatalf("changed repeat = %#v", changed)
	}
	pending, err := manager.SendFromMainAgentTurn(context.Background(), "mainAgent", "turn-2", record.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Action != toolexecution.SubagentSendResultPending || pending.Accepted || len(pending.Subagent.Pending) != 0 {
		t.Fatalf("next mainAgent turn = %#v", pending)
	}

	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	if got := model.Requests(); len(got) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(got))
	}
}

func TestSubagentManagerIdleSendWaitsForLatestResultObservation(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 1)}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	results := manager.subscribeResults(context.Background())
	record, err := manager.Start(context.Background(), "mainAgent", "start-turn", "researcher", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	var result SubagentResult
	select {
	case result = <-results:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subagent result")
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	if result.Status != SubagentResultCompleted {
		t.Fatalf("result status = %q, want completed", result.Status)
	}
	if _, err := manager.Send(context.Background(), "mainAgent", record.ID, "follow up"); !errors.Is(err, storage.ErrSubagentResultPending) {
		t.Fatalf("direct send before result observation error = %v", err)
	}
	sameTurn, err := manager.SendFromMainAgentTurn(context.Background(), "mainAgent", "start-turn", record.ID, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if sameTurn.Action != toolexecution.SubagentSendAlreadySent || sameTurn.Accepted {
		t.Fatalf("same-turn send after result = %#v, want already_sent", sameTurn)
	}
	pending, err := manager.SendFromMainAgentTurn(context.Background(), "mainAgent", "early-turn", record.ID, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Action != toolexecution.SubagentSendResultPending || pending.Accepted || pending.Subagent.Status != storage.SubagentStatusIdle {
		t.Fatalf("model send before result observation = %#v", pending)
	}
	observeTestSubagentResult(t, manager, result)
	sent, err := manager.SendFromMainAgentTurn(context.Background(), "mainAgent", "result-turn", record.ID, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if sent.Action != toolexecution.SubagentSendCompleted || sent.Accepted || sent.Subagent.Status != storage.SubagentStatusIdle {
		t.Fatalf("completed result cannot resume = %#v", sent)
	}
}

func TestSubagentManagerDoesNotReuseCompletedSubagent(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 1)}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	results := manager.subscribeResults(context.Background())
	record, err := manager.Start(context.Background(), "mainAgent", "start-turn", "researcher", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	select {
	case <-results:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subagent result")
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	observeTestSubagentResult(t, manager, markTestSubagentCompleted(t, manager, record.ID))

	if _, err := manager.Send(context.Background(), "mainAgent", record.ID, "unrelated next task"); !errors.Is(err, storage.ErrSubagentCompleted) {
		t.Fatalf("direct completed-subagent send error = %v", err)
	}
	result, err := manager.SendFromMainAgentTurn(context.Background(), "mainAgent", "follow-up-turn", record.ID, "unrelated next task")
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != toolexecution.SubagentSendCompleted || result.Accepted {
		t.Fatalf("model completed-subagent send = %#v", result)
	}
	if got := model.Requests(); len(got) != 1 {
		t.Fatalf("provider requests = %d, want only the initial subagent turn", len(got))
	}
}

func TestSubagentManagerLimitsRepeatedFailedRecoveryWithinResponseScope(t *testing.T) {
	manager := newTestSubagentManager(t, subagentFailModel{err: errors.New("request 133409 tokens exceeds context limit 131072")}, 1)
	defer manager.Close()
	results := manager.subscribeResults(context.Background())
	if err := manager.mainAgent.responseScopes.BeginMainAgentTurn("mainAgent", "root-turn"); err != nil {
		t.Fatal(err)
	}
	record, err := manager.Start(context.Background(), "mainAgent", "root-turn", "researcher", "inspect project", "")
	if err != nil {
		t.Fatal(err)
	}

	firstResult := waitTestSubagentResult(t, results)
	if firstResult.Status != SubagentResultFailed {
		t.Fatalf("first result status = %q, want failed", firstResult.Status)
	}
	observeTestSubagentResult(t, manager, firstResult)
	firstReservation, err := manager.mainAgent.responseScopes.ReserveResultTurn(
		"mainAgent", "result-1", record.ID, firstResult.SubagentTurnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstReservation.Commit()

	firstRecovery, err := manager.SendFromMainAgentTurn(context.Background(), "mainAgent", "result-1", record.ID, "retry after compacting")
	if err != nil {
		t.Fatal(err)
	}
	if firstRecovery.Action != toolexecution.SubagentSendStarted || !firstRecovery.Accepted {
		t.Fatalf("first recovery = %#v", firstRecovery)
	}

	secondResult := waitTestSubagentResult(t, results)
	if secondResult.Status != SubagentResultFailed {
		t.Fatalf("second result status = %q, want failed", secondResult.Status)
	}
	observeTestSubagentResult(t, manager, secondResult)
	secondReservation, err := manager.mainAgent.responseScopes.ReserveResultTurn(
		"mainAgent", "result-2", record.ID, secondResult.SubagentTurnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondReservation.Commit()

	exhausted, err := manager.SendFromMainAgentTurn(context.Background(), "mainAgent", "result-2", record.ID, "retry once more")
	if err != nil {
		t.Fatal(err)
	}
	if exhausted.Action != toolexecution.SubagentSendRecoveryExhausted || exhausted.Accepted {
		t.Fatalf("repeated recovery = %#v", exhausted)
	}
}

func TestSubagentFailureFingerprintNormalizesChangingCounts(t *testing.T) {
	first := subagentFailureFingerprint("Request 133409 tokens exceeds context limit 131072")
	second := subagentFailureFingerprint(" request 133670 tokens exceeds context limit 131072 ")
	if first != second || first != "request # tokens exceeds context limit #" {
		t.Fatalf("fingerprints = (%q, %q)", first, second)
	}
}

func TestSubagentManagerStartAlwaysCreatesNewSubagent(t *testing.T) {
	t.Run("same definition creates separately addressed subagent", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 3)
		defer manager.Close()
		first, err := manager.Start(context.Background(), "mainAgent", "turn-1", "researcher", "first", "")
		if err != nil {
			t.Fatal(err)
		}
		started, err := manager.Start(context.Background(), "mainAgent", "turn-2", "researcher", "talk more", "")
		if err != nil {
			t.Fatal(err)
		}
		if started.ID == first.ID || started.DisplayName == first.DisplayName {
			t.Fatalf("started = %#v, first = %#v", started, first)
		}
		subagents, err := manager.List(context.Background(), "mainAgent", false)
		if err != nil || len(subagents) != 2 {
			t.Fatalf("subagents = %#v, %v", subagents, err)
		}
	})

	t.Run("many open subagents do not require selection", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 3)
		defer manager.Close()
		first, err := manager.Start(context.Background(), "mainAgent", "turn-1", "researcher", "first", "")
		if err != nil {
			t.Fatal(err)
		}
		second, err := manager.Start(context.Background(), "mainAgent", "turn-2", "researcher", "second", "")
		if err != nil {
			t.Fatal(err)
		}
		if first.DisplayName == "" || second.DisplayName == "" || first.DisplayName == second.DisplayName {
			t.Fatalf("friendly names = %q and %q", first.DisplayName, second.DisplayName)
		}
		third, err := manager.Start(context.Background(), "mainAgent", "turn-3", "researcher", "talk more", "")
		if err != nil {
			t.Fatal(err)
		}
		if third.ID == first.ID || third.ID == second.ID || third.DisplayName == first.DisplayName || third.DisplayName == second.DisplayName {
			t.Fatalf("third = %#v, first = %#v, second = %#v", third, first, second)
		}
		subagents, err := manager.List(context.Background(), "mainAgent", false)
		if err != nil || len(subagents) != 3 {
			t.Fatalf("subagents = %#v, %v", subagents, err)
		}
	})

	t.Run("same definition always creates", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 3)
		defer manager.Close()
		first, err := manager.Start(context.Background(), "mainAgent", "turn-1", "researcher", "first", "")
		if err != nil {
			t.Fatal(err)
		}
		started, err := manager.Start(context.Background(), "mainAgent", "turn-2", "researcher", "parallel", "")
		if err != nil {
			t.Fatal(err)
		}
		if started.ID == first.ID || started.DisplayName == first.DisplayName {
			t.Fatalf("started = %#v, first = %#v", started, first)
		}
	})

	t.Run("different definition creates", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 3)
		defer manager.Close()
		first, err := manager.Start(context.Background(), "mainAgent", "turn-1", "researcher", "first", "")
		if err != nil {
			t.Fatal(err)
		}
		started, err := manager.Start(context.Background(), "mainAgent", "turn-2", "reviewer", "review separately", "")
		if err != nil {
			t.Fatal(err)
		}
		if started.ID == first.ID || started.DefinitionName != "reviewer" {
			t.Fatalf("started = %#v, first = %#v", started, first)
		}
		subagents, err := manager.List(context.Background(), "mainAgent", false)
		if err != nil || len(subagents) != 2 {
			t.Fatalf("subagents = %#v, %v", subagents, err)
		}
	})
}

func TestSubagentManagerStartWaitsForInitialInputCommit(t *testing.T) {
	messages := &subagentInitialAppendStorage{
		MessageStorage: inmemory.NewMessageStorage(), entered: make(chan struct{}), release: make(chan struct{}),
	}
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManagerWithStorage(t, model, 1, messages)
	defer manager.Close()

	type result struct {
		record storage.Subagent
		err    error
	}
	returned := make(chan result, 1)
	go func() {
		record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "visible", "")
		returned <- result{record: record, err: err}
	}()
	select {
	case <-messages.entered:
	case <-time.After(time.Second):
		t.Fatal("subagent did not attempt its initial append")
	}
	select {
	case outcome := <-returned:
		t.Fatalf("Start returned before input append committed: %#v", outcome)
	default:
	}
	close(messages.release)
	var outcome result
	select {
	case outcome = <-returned:
	case <-time.After(time.Second):
		t.Fatal("Start did not return after input append committed")
	}
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	read, err := manager.Read(context.Background(), "mainAgent", outcome.record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if read.FinalAnswer != nil || read.LastMessageID == "" {
		t.Fatalf("Read immediately after Start = %#v, want no final answer and an advanced cursor", read)
	}
}

func TestSubagentManagerRetainsLastTurnFailure(t *testing.T) {
	providerErr := errors.New("provider failed before answering")
	manager := newTestSubagentManager(t, subagentFailModel{err: providerErr}, 1)
	defer manager.Close()

	record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "inspect project", "")
	if err != nil {
		t.Fatal(err)
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	idle, err := manager.getOwned(context.Background(), "mainAgent", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if idle.LastSubagentTurnID != record.CurrentSubagentTurnID || !strings.Contains(idle.LastResultError, providerErr.Error()) {
		t.Fatalf("idle failure = %#v", idle)
	}
}

func TestSubagentManagerPublishesCompactSuccessAndFailureResults(t *testing.T) {
	t.Run("success includes final assistant answer", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 1)
		defer manager.Close()
		results := manager.subscribeResults(context.Background())
		record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "work", "")
		if err != nil {
			t.Fatal(err)
		}
		model.releases <- struct{}{}
		select {
		case result := <-results:
			if result.SubagentID != record.ID || result.DisplayName != record.DisplayName || result.Status != SubagentResultCompleted || result.Error != "" || result.FinalAnswer == nil || result.FinalAnswer.Content != "done" || result.LastMessageID == "" {
				t.Fatalf("result = %#v", result)
			}
			message := result.RuntimeMessage()
			for _, expected := range []string{"authoritative subagent result", "final_answer or summary", "one focused follow-up", "never duplicate", "poll"} {
				if !strings.Contains(message.Content, expected) {
					t.Fatalf("result instruction missing %q: %s", expected, message.Content)
				}
			}
			if strings.Contains(message.Content, "close_subagent") {
				t.Fatalf("removed destructive tool appears in result instruction: %s", message.Content)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for success result")
		}
	})

	t.Run("failure includes terminal error", func(t *testing.T) {
		manager := newTestSubagentManager(t, subagentFailModel{err: errors.New("provider unavailable")}, 1)
		defer manager.Close()
		results := manager.subscribeResults(context.Background())
		record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "work", "")
		if err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-results:
			if result.SubagentID != record.ID || result.Status != SubagentResultFailed || !strings.Contains(result.Error, "provider unavailable") || result.FinalAnswer != nil {
				t.Fatalf("result = %#v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for failure result")
		}
	})
}

func TestSubagentManagerReadDefaultsToObservedCursorAndFinalAnswerOnly(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)

	first, err := manager.Read(context.Background(), "mainAgent", record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.FinalAnswer == nil || first.FinalAnswer.Content != "done" || first.LastMessageID == "" {
		t.Fatalf("first read = %#v", first)
	}
	second, err := manager.Read(context.Background(), "mainAgent", record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.FinalAnswer != nil || second.LastMessageID != first.LastMessageID {
		t.Fatalf("second read replayed output: %#v", second)
	}
}

func TestSubagentManagerReadOwnershipWaitAndClose(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "mainAgent-a", "mainAgent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Read(context.Background(), "mainAgent-b", record.ID, ""); !errors.Is(err, storage.ErrSubagentNotFound) {
		t.Fatalf("cross-mainAgent Read error = %v", err)
	}
	read, err := manager.Read(context.Background(), "mainAgent-a", record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if read.FinalAnswer != nil || read.LastMessageID == "" || read.Subagent.ObservedMessageID != read.LastMessageID {
		t.Fatalf("read result = %#v", read)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Wait(canceled, "mainAgent-a", []string{record.ID}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait cancellation error = %v", err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	closed, err := manager.CloseSubagent(context.Background(), "mainAgent-a", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Subagent.Status != storage.SubagentStatusClosed {
		t.Fatalf("closed record = %#v", closed)
	}
	if _, err := manager.Send(context.Background(), "mainAgent-a", record.ID, "again"); !errors.Is(err, storage.ErrSubagentClosed) {
		t.Fatalf("Send closed error = %v", err)
	}
	// Closing preserves the subagent transcript for later nested-chat rendering.
	if _, err := manager.Read(context.Background(), "mainAgent-a", record.ID, ""); err != nil {
		t.Fatalf("Read closed history error = %v", err)
	}
}

func TestResponseScopeAutoClosesCompletedAndFailedSubagents(t *testing.T) {
	tests := []struct {
		name        string
		model       agentruntime.Model
		release     chan struct{}
		complete    bool
		wantStatus  storage.SubagentStatus
		wantOutcome storage.SubagentResultStatus
	}{
		{name: "completed", model: &subagentGateModel{releases: make(chan struct{})}, complete: true, wantStatus: storage.SubagentStatusClosed, wantOutcome: storage.SubagentResultCompleted},
		{name: "failed", model: subagentFailModel{err: errors.New("provider failed")}, wantStatus: storage.SubagentStatusClosed, wantOutcome: storage.SubagentResultFailed},
	}
	for index := range tests {
		test := tests[index]
		t.Run(test.name, func(t *testing.T) {
			if gate, ok := test.model.(*subagentGateModel); ok {
				test.release = gate.releases
			}
			manager := newTestSubagentManager(t, test.model, 1)
			defer manager.Close()
			manager.mainAgent.responseScopes.SetCleanup(manager.autoCloseScopeSubagents)
			systemEvents := manager.subscribeSystemEvents(context.Background())
			if err := manager.mainAgent.responseScopes.BeginMainAgentTurn("mainAgent", "root-turn"); err != nil {
				t.Fatal(err)
			}
			record, err := manager.Start(context.Background(), "mainAgent", "root-turn", "researcher", "work", "")
			if err != nil {
				t.Fatal(err)
			}
			if test.release != nil {
				test.release <- struct{}{}
			}
			awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
			current, _, err := manager.store.Get(context.Background(), record.ID)
			if err != nil {
				t.Fatal(err)
			}
			var result SubagentResult
			if test.complete {
				result = markTestSubagentCompleted(t, manager, record.ID)
			} else {
				messages, listErr := manager.mainAgent.ListMessages(context.Background(), current.SubagentSessionID)
				if listErr != nil {
					t.Fatal(listErr)
				}
				result = subagentResultFromMessages(current, messages)
			}
			observeTestSubagentResult(t, manager, result)
			reservation, err := manager.mainAgent.responseScopes.ReserveResultTurn("mainAgent", "result-turn", record.ID, result.SubagentTurnID)
			if err != nil {
				t.Fatal(err)
			}
			reservation.Commit()
			manager.mainAgent.responseScopes.FinishTurn("mainAgent", "root-turn")
			before, _, err := manager.store.Get(context.Background(), record.ID)
			if err != nil || before.Status != storage.SubagentStatusIdle {
				t.Fatalf("subagent closed before result continuation ended: %#v, %v", before, err)
			}
			manager.mainAgent.responseScopes.FinishTurn("mainAgent", "result-turn")
			after, _, err := manager.store.Get(context.Background(), record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != test.wantStatus || after.LastResultStatus != test.wantOutcome {
				t.Fatalf("scope-end subagent = %#v, want status=%s outcome=%s", after, test.wantStatus, test.wantOutcome)
			}
			if test.wantStatus == storage.SubagentStatusClosed {
				select {
				case event := <-systemEvents:
					closed := event.SubagentClosed
					if event.Type != SystemSubagentClosed || event.MainAgentSessionID != "mainAgent" || event.MainAgentTurnID != "root-turn" ||
						closed == nil || !closed.Automatic || closed.Subagent.ID != record.ID ||
						closed.PreviousStatus != storage.SubagentStatusIdle ||
						closed.PreviousResultStatus != test.wantOutcome {
						t.Fatalf("automatic close event = %#v", event)
					}
				case <-time.After(time.Second):
					t.Fatal("timed out waiting for automatic close event")
				}
			}
		})
	}
}

func TestSubagentManagerCloseRetainsRunsAfterReleasingSubagent(t *testing.T) {
	t.Run("completed run remains available for SSE backfill", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 1)
		defer manager.Close()
		record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "complete", "")
		if err != nil {
			t.Fatal(err)
		}
		run, err := manager.Run(context.Background(), "mainAgent", record.ID, record.CurrentSubagentTurnID)
		if err != nil {
			t.Fatal(err)
		}
		model.releases <- struct{}{}
		awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
		observeTestSubagentResult(t, manager, markTestSubagentCompleted(t, manager, record.ID))
		idle, err := manager.getOwned(context.Background(), "mainAgent", record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CloseSubagent(context.Background(), "mainAgent", record.ID); err != nil {
			t.Fatal(err)
		}
		retained, err := manager.Run(context.Background(), "mainAgent", record.ID, idle.LastSubagentTurnID)
		if err != nil {
			t.Fatal(err)
		}
		if retained != run || len(retained.Events()) == 0 {
			t.Fatalf("retained completed run = %#v, want original event history", retained)
		}
	})

	t.Run("user-directed close interrupts active subagent and preserves its run", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 1)
		defer manager.Close()
		record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "active", "")
		if err != nil {
			t.Fatal(err)
		}
		run, err := manager.Run(context.Background(), "mainAgent", record.ID, record.CurrentSubagentTurnID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := manager.CloseSubagent(context.Background(), "mainAgent", record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Interrupted || result.Subagent.Status != storage.SubagentStatusClosed {
			t.Fatalf("active close result = %#v", result)
		}
		waitRun(t, run)
		retained, err := manager.Run(context.Background(), "mainAgent", record.ID, record.CurrentSubagentTurnID)
		if err != nil {
			t.Fatal(err)
		}
		if retained != run || !retained.Done() || len(retained.Events()) == 0 {
			t.Fatalf("retained active run = %#v, want completed original", retained)
		}
	})
}

func TestSubagentManagerCloseInterruptsAndDropsQueuedWork(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	results := manager.subscribeResults(context.Background())
	systemEvents := manager.subscribeSystemEvents(context.Background())

	record, err := manager.Start(context.Background(), "mainAgent", "start-turn", "researcher", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.Run(context.Background(), "mainAgent", record.ID, record.CurrentSubagentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := manager.Send(context.Background(), "mainAgent", record.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Pending) != 1 {
		t.Fatalf("queued messages = %d, want 1", len(queued.Pending))
	}

	result, err := manager.CloseSubagent(context.Background(), "mainAgent", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Subagent.Status != storage.SubagentStatusClosed || result.PreviousStatus != storage.SubagentStatusRunning || result.PreviousResultStatus != "" || result.DroppedMessages != 1 || !result.Interrupted {
		t.Fatalf("close result = %#v", result)
	}
	select {
	case event := <-systemEvents:
		closed := event.SubagentClosed
		if event.Type != SystemSubagentClosed || event.MainAgentSessionID != "mainAgent" || event.MainAgentTurnID != "" ||
			closed == nil || closed.Subagent.ID != record.ID ||
			closed.Subagent.Status != storage.SubagentStatusClosed ||
			closed.PreviousStatus != storage.SubagentStatusRunning ||
			closed.PreviousResultStatus != "" || closed.DroppedMessages != 1 ||
			!closed.Interrupted || closed.Automatic {
			t.Fatalf("closed event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subagent closed event")
	}
	if len(result.Subagent.Pending) != 0 || result.Subagent.ClosedAt == nil {
		t.Fatalf("closed subagent = %#v", result.Subagent)
	}
	if _, err := manager.Send(context.Background(), "mainAgent", record.ID, "after close"); !errors.Is(err, storage.ErrSubagentClosed) {
		t.Fatalf("send after close error = %v", err)
	}
	retained, err := manager.Run(context.Background(), "mainAgent", record.ID, record.CurrentSubagentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, retained)
	if retained != run || !retained.Done() || len(retained.Events()) == 0 {
		t.Fatalf("retained closed run mismatch: same=%t done=%t events=%d", retained == run, retained.Done(), len(retained.Events()))
	}
	messages, err := manager.mainAgent.ListMessages(context.Background(), record.SubagentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 || messages[0].Content != "first" {
		t.Fatalf("retained subagent transcript = %#v", messages)
	}
	select {
	case result := <-results:
		t.Fatalf("closed subagent published result: %#v", result)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestSubagentManagerCloseCancelsOutstandingResponseScopeResults(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	if err := manager.mainAgent.responseScopes.BeginMainAgentTurn("mainAgent", "mainAgent-turn"); err != nil {
		t.Fatal(err)
	}
	record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	subagentTurnID := record.CurrentSubagentTurnID
	if manager.mainAgent.responseScopes.ReadyToEnd("mainAgent", "mainAgent-turn") {
		t.Fatal("mainAgent scope became ready while the subagent result was pending")
	}

	if _, err := manager.CloseSubagent(context.Background(), "mainAgent", record.ID); err != nil {
		t.Fatal(err)
	}
	if !manager.mainAgent.responseScopes.ReadyToEnd("mainAgent", "mainAgent-turn") {
		t.Fatal("application close left an impossible result obligation in the mainAgent scope")
	}
	if _, err := manager.mainAgent.responseScopes.ReserveResultTurn(
		"mainAgent",
		"result-turn",
		record.ID,
		subagentTurnID,
	); !errors.Is(err, toolexecution.ErrResponseScopeAssignmentNotFound) {
		t.Fatalf("result reservation after close error = %v, want ErrResponseScopeAssignmentNotFound", err)
	}
}

func newTestSubagentManager(t *testing.T, model agentruntime.Model, maximum int) *subagentManager {
	return newTestSubagentManagerWithStorage(t, model, maximum, inmemory.NewMessageStorage())
}

func markTestSubagentCompleted(t *testing.T, manager *subagentManager, id string) SubagentResult {
	t.Helper()
	record, found, err := manager.store.Get(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("get subagent for completion = (%#v, %v, %v)", record, found, err)
	}
	completed, err := manager.store.Update(context.Background(), id, record.Version, storage.SubagentUpdate{
		Status:                record.Status,
		CurrentSubagentTurnID: record.CurrentSubagentTurnID,
		LastSubagentTurnID:    record.LastSubagentTurnID,
		LastResultStatus:      storage.SubagentResultCompleted,
		LastResultSummary:     "test work completed",
		LastResultNextStep:    "",
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := manager.mainAgent.ListMessages(context.Background(), record.SubagentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	return subagentResultFromMessages(completed, messages)
}

func observeTestSubagentResult(t *testing.T, manager *subagentManager, result SubagentResult) {
	t.Helper()
	if err := manager.observeSubagentResult(context.Background(), result); err != nil {
		t.Fatal(err)
	}
}

func waitTestSubagentResult(t *testing.T, results <-chan SubagentResult) SubagentResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subagent result")
		return SubagentResult{}
	}
}

func newTestSubagentManagerWithStorage(t *testing.T, model agentruntime.Model, maximum int, messages storage.MessageStorage) *subagentManager {
	t.Helper()
	permissions := inmemory.NewPermissionStorage()
	mainAgent, err := New(context.Background(), WithModel(&scriptedModel{}), WithMessageStorage(messages), WithPermissionStorage(permissions))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mainAgent.Close() })
	manager, err := newSubagentManager(mainAgent, config{
		project: &Project{subagents: map[string]SubagentDefinition{
			"researcher": {Name: "researcher", Description: "Research", Provider: "test", Model: "test", Instructions: "be useful"},
			"reviewer":   {Name: "reviewer", Description: "Review", Provider: "test", Model: "test", Instructions: "review carefully"},
		}},
		messages: messages, permissions: permissions, subagents: inmemory.NewSubagentStorage(),
		maxSubagents: maximum, permissionMode: mainAgent.PermissionMode(), permissionPolicy: permission.Policy{Mode: mainAgent.PermissionMode()},
		toolWorkers: defaultToolWorkers, channelBuffer: defaultChannelBuffer, skillReload: DefaultSkillReloadPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.subagentFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(), WithModel(model), WithMessageStorage(messages), WithPermissionStorage(permissions))
	}
	return manager
}

type subagentInitialAppendStorage struct {
	storage.MessageStorage
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *subagentInitialAppendStorage) Append(ctx context.Context, messages ...storage.Message) error {
	s.once.Do(func() {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	return s.MessageStorage.Append(ctx, messages...)
}

func awaitSubagentStatus(t *testing.T, manager *subagentManager, id string, status storage.SubagentStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		record, found, err := manager.store.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if found && record.Status == status {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("subagent %s did not reach %s", id, status)
}

// subagentGateModel completes one provider round only after a matching test
// release, making it possible to assert the manager's asynchronous boundary.
type subagentGateModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
	releases chan struct{}
}

func (m *subagentGateModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	return subagentGateStream{release: m.releases}, nil
}

func (m *subagentGateModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}

func (m *subagentGateModel) waitStarts(want int) error {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(m.Requests()) >= want {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return errors.New("subagent provider did not start")
}

type subagentGateStream struct{ release <-chan struct{} }

type subagentFailModel struct{ err error }

func (model subagentFailModel) Start(context.Context, agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	return nil, model.err
}

func (s subagentGateStream) Subscribe(ctx context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	go func() {
		defer close(events)
		select {
		case <-s.release:
			events <- provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}}}
		case <-ctx.Done():
		}
	}()
	return events
}

func (subagentGateStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, errors.New("unused")
}
