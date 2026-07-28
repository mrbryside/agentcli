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
	awaitSubagentStatus(t, manager, record.ID, "")

	requests := model.Requests()
	if len(requests) != 2 || requests[0].Messages[len(requests[0].Messages)-1].Content != "first" || requests[1].Messages[len(requests[1].Messages)-1].Content != "second" {
		t.Fatalf("subagent requests = %#v", requests)
	}
}

func TestSubagentManagerExecuteTaskWaitsForFinalAssistantResponse(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 1)}
	manager := newTestSubagentManager(t, model, 2)
	defer manager.Close()
	// A positive wait limit still returns a foreground result when the child
	// completes before the timer; it must not install a delivery identity.
	manager.config.taskForegroundWait = time.Second
	events := manager.subscribeSystemEvents(context.Background())
	if err := manager.mainAgent.responseScopes.BeginMainAgentTurn("mainAgent", "main-turn"); err != nil {
		t.Fatal(err)
	}

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
		if record.Status != "" || record.ActiveTaskDelivery != nil {
			t.Fatalf("foreground task persisted delivery/state = %#v", record)
		}
		if !manager.mainAgent.responseScopes.ReadyToEnd("mainAgent", "main-turn") {
			t.Fatal("foreground task incorrectly registered a later-result scope barrier")
		}
		select {
		case event := <-events:
			if event.Type != SystemTaskCompleted || event.MainAgentTurnID != "main-turn" || event.TaskCompleted == nil ||
				event.TaskCompleted.TaskID != result.TaskID || event.TaskCompleted.State != TaskStateCompleted {
				t.Fatalf("foreground task completion event = %#v", event)
			}
		case <-time.After(time.Second):
			t.Fatal("foreground task did not publish completion event")
		}
	case <-time.After(time.Second):
		t.Fatal("foreground task did not return after child completion")
	}
}

func TestSubagentManagerRehydratesRetainedTaskByExactID(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 2)}
	messages := inmemory.NewMessageStorage()
	relationships := inmemory.NewSubagentStorage()
	manager := newTestSubagentManagerWithStores(t, model, messages, relationships)
	if err := manager.mainAgent.responseScopes.BeginMainAgentTurn("mainAgent", "first-turn"); err != nil {
		t.Fatal(err)
	}

	type taskOutcome struct {
		result TaskResult
		err    error
	}
	firstResult := make(chan taskOutcome, 1)
	go func() {
		result, err := manager.ExecuteTask(context.Background(), TaskRequest{
			MainAgentSessionID: "mainAgent", MainAgentTurnID: "first-turn",
			AgentName: "researcher", Description: "Retained work", Prompt: "first request",
		})
		firstResult <- taskOutcome{result: result, err: err}
	}()
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	firstOutcome := <-firstResult
	if firstOutcome.err != nil {
		t.Fatal(firstOutcome.err)
	}
	first := firstOutcome.result
	if first.TaskID == "" || first.State != TaskStateCompleted {
		t.Fatalf("first task result = %#v", first)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	resumedManager := newTestSubagentManagerWithStores(t, model, messages, relationships)
	defer resumedManager.Close()
	if err := resumedManager.mainAgent.responseScopes.BeginMainAgentTurn("mainAgent", "second-turn"); err != nil {
		t.Fatal(err)
	}
	resumedResult := make(chan taskOutcome, 1)
	go func() {
		result, err := resumedManager.ExecuteTask(context.Background(), TaskRequest{
			MainAgentSessionID: "mainAgent", MainAgentTurnID: "second-turn",
			TaskID: first.TaskID, Prompt: "second request",
		})
		resumedResult <- taskOutcome{result: result, err: err}
	}()
	if err := model.waitStarts(2); err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	resumedOutcome := <-resumedResult
	if resumedOutcome.err != nil {
		t.Fatal(resumedOutcome.err)
	}
	resumed := resumedOutcome.result
	if resumed.TaskID != first.TaskID || resumed.State != TaskStateCompleted {
		t.Fatalf("resumed task result = %#v", resumed)
	}
	requests := model.Requests()
	if got := requests[len(requests)-1].Messages[len(requests[len(requests)-1].Messages)-1].Content; got != "second request" {
		t.Fatalf("resumed task latest message = %q", got)
	}
}

func TestSubagentManagerShutdownRetainsRunningTaskForLaterResume(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	messages := inmemory.NewMessageStorage()
	relationships := inmemory.NewSubagentStorage()
	manager := newTestSubagentManagerWithStores(t, model, messages, relationships)
	record, err := manager.Start(context.Background(), "mainAgent", "main-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setTaskDelivery(context.Background(), record.ID, &storage.TaskDelivery{
		MainAgentTurnID: "main-turn",
		AssignmentID:    record.CurrentSubagentTurnID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	retained, found, err := relationships.Get(context.Background(), record.ID)
	if err != nil || !found {
		t.Fatalf("retained task after shutdown = %#v, found=%v, err=%v", retained, found, err)
	}
	if retained.Status == storage.SubagentStatusClosed {
		t.Fatalf("shutdown wrote a closed tombstone: %#v", retained)
	}
	if retained.Status == storage.SubagentStatusRunning || retained.CurrentSubagentTurnID != "" {
		t.Fatalf("shutdown left an unresumable running task: %#v", retained)
	}
	if retained.ActiveTaskDelivery != nil {
		t.Fatalf("shutdown retained process-local result delivery: %#v", retained.ActiveTaskDelivery)
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

func TestSubagentManagerExecuteTaskBackgroundRegistersOneDeliveryAndPublishesOneTerminalEvent(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 1)}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	if err := manager.mainAgent.responseScopes.BeginMainAgentTurn("mainAgent", "root-turn"); err != nil {
		t.Fatal(err)
	}
	events := manager.subscribeSystemEvents(context.Background())
	result, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "mainAgent", MainAgentTurnID: "root-turn", AgentName: "researcher",
		Description: "Long work", Prompt: "wait", Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != TaskStateRunning || result.TaskID == "" {
		t.Fatalf("background start result = %#v", result)
	}
	record, found, err := manager.store.Get(context.Background(), result.TaskID)
	if err != nil || !found || record.ActiveTaskDelivery == nil ||
		record.ActiveTaskDelivery.MainAgentTurnID != "root-turn" ||
		record.ActiveTaskDelivery.AssignmentID != record.CurrentSubagentTurnID {
		t.Fatalf("background delivery record = (%#v, %t, %v)", record, found, err)
	}
	if manager.mainAgent.responseScopes.ReadyToEnd("mainAgent", "root-turn") {
		t.Fatal("background task did not hold one response-scope delivery barrier")
	}
	run, err := manager.Run(context.Background(), "mainAgent", result.TaskID, record.CurrentSubagentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.registerTaskDelivery(context.Background(), record, manager.project.subagents["researcher"], run, *record.ActiveTaskDelivery); err != nil {
		t.Fatalf("duplicate task delivery registration: %v", err)
	}
	model.releases <- struct{}{}
	select {
	case event := <-events:
		if event.Type != SystemTaskCompleted || event.MainAgentTurnID != "root-turn" || event.TaskCompleted == nil ||
			event.TaskCompleted.TaskID != result.TaskID || event.TaskCompleted.State != TaskStateCompleted {
			t.Fatalf("task completion event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task completion event")
	}
	select {
	case duplicate := <-events:
		t.Fatalf("duplicate task completion event = %#v", duplicate)
	case <-time.After(25 * time.Millisecond):
	}
	awaitSubagentStatus(t, manager, result.TaskID, "")
	record, found, err = manager.store.Get(context.Background(), result.TaskID)
	if err != nil || !found || record.ActiveTaskDelivery != nil {
		t.Fatalf("terminal background record = (%#v, %t, %v)", record, found, err)
	}
	reservation, err := manager.mainAgent.responseScopes.ReserveResultTurn("mainAgent", "task-result-turn", result.TaskID, record.LastSubagentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if progress := reservation.ResultProgress(); progress.PendingCount != 0 || !progress.AllResultsDelivered {
		t.Fatalf("duplicate delivery left extra scope obligations: %#v", progress)
	}
	reservation.Commit()
}

func TestSubagentManagerExecuteTaskPromotionRegistersDeliveryExactlyOnce(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 1)}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	manager.config.taskForegroundWait = time.Millisecond
	if err := manager.mainAgent.responseScopes.BeginMainAgentTurn("mainAgent", "root-turn"); err != nil {
		t.Fatal(err)
	}
	events := manager.subscribeSystemEvents(context.Background())
	result, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "mainAgent", MainAgentTurnID: "root-turn", AgentName: "researcher",
		Description: "Long work", Prompt: "wait",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != TaskStateRunning {
		t.Fatalf("promoted task result = %#v", result)
	}
	model.releases <- struct{}{}
	select {
	case event := <-events:
		if event.Type != SystemTaskCompleted || event.TaskCompleted == nil || event.TaskCompleted.TaskID != result.TaskID {
			t.Fatalf("promoted completion event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for promoted task completion")
	}
	select {
	case duplicate := <-events:
		t.Fatalf("duplicate promoted task completion event = %#v", duplicate)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestSubagentManagerExecuteTaskBackgroundResumeUsesLatestDeliveryTurn(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 2)}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	for _, turnID := range []string{"first-turn", "latest-turn"} {
		if err := manager.mainAgent.responseScopes.BeginMainAgentTurn("owner", turnID); err != nil {
			t.Fatal(err)
		}
	}
	events := manager.subscribeSystemEvents(context.Background())
	first, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "owner", MainAgentTurnID: "first-turn", AgentName: "researcher",
		Description: "Initial work", Prompt: "first", Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("first background task did not complete")
	}
	awaitSubagentStatus(t, manager, first.TaskID, "")

	resumed, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "owner", MainAgentTurnID: "latest-turn", TaskID: first.TaskID,
		Prompt: "continue", Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.TaskID != first.TaskID || resumed.State != TaskStateRunning {
		t.Fatalf("background resume = %#v", resumed)
	}
	record, found, err := manager.store.Get(context.Background(), first.TaskID)
	if err != nil || !found || record.ActiveTaskDelivery == nil || record.ActiveTaskDelivery.MainAgentTurnID != "latest-turn" {
		t.Fatalf("resumed active delivery = (%#v, %t, %v)", record, found, err)
	}
	model.releases <- struct{}{}
	select {
	case event := <-events:
		if event.Type != SystemTaskCompleted || event.MainAgentTurnID != "latest-turn" || event.TaskCompleted == nil || event.TaskCompleted.TaskID != first.TaskID {
			t.Fatalf("resumed task completion = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed background task did not complete")
	}
}

func TestSubagentManagerTaskCompletionPublishesContractMetadataOnlyAsSystemEvent(t *testing.T) {
	manager := newTestSubagentManager(t, taskFinalModel{content: `{"message":"Need your confirmation","requires_requester_reply":true}`}, 1)
	defer manager.Close()
	definition := manager.project.subagents["researcher"]
	definition.Result = &AgentResultContract{
		MessageField: "message",
		Metadata: map[string]AgentResultMetadataField{
			"requires_requester_reply": {Type: "boolean", Required: true},
		},
	}
	manager.project.subagents["researcher"] = definition
	events := manager.subscribeSystemEvents(context.Background())
	result, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "mainAgent", MainAgentTurnID: "root-turn", AgentName: "researcher",
		Description: "Ask one question", Prompt: "work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != TaskStateCompleted || result.Output != "Need your confirmation" {
		t.Fatalf("contract task result = %#v", result)
	}
	select {
	case event := <-events:
		if event.Type != SystemTaskCompleted || event.TaskCompleted == nil || event.TaskCompleted.Metadata["requires_requester_reply"] != true {
			t.Fatalf("contract task system event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("contract task did not publish metadata event")
	}
}

func TestSubagentManagerExecuteTaskResumesOnlyOwnedRetainedTask(t *testing.T) {
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
	firstInstance, err := manager.instance(first.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	firstInstance.mu.Lock()
	firstRuntimeReleased := firstInstance.agent == nil
	firstInstance.mu.Unlock()
	if !firstRuntimeReleased {
		t.Fatal("completed task retained a live subagent runtime")
	}
	crossSession, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "other", MainAgentTurnID: "other-turn", TaskID: first.TaskID, Prompt: "continue",
	})
	if err != nil || crossSession.State != TaskStateError || crossSession.ErrorCode != TaskErrorNotFound {
		t.Fatalf("cross-session resume result = %#v, err = %v", crossSession, err)
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
	manager.config.taskForegroundWait = time.Second
	events := manager.subscribeSystemEvents(context.Background())
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
	select {
	case event := <-events:
		if event.Type != SystemTaskCompleted || event.TaskCompleted == nil || event.TaskCompleted.State != TaskStateError ||
			!strings.Contains(event.TaskCompleted.TaskID, "subagent_") {
			t.Fatalf("cancelled task completion event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled task did not publish terminal event")
	}
	records, err := manager.List(context.Background(), "mainAgent", true)
	if err != nil || len(records) != 1 || records[0].ActiveTaskDelivery != nil {
		t.Fatalf("cancelled foreground wait registered delivery = (%#v, %v)", records, err)
	}
}

func TestSubagentManagerExecuteTaskReportsUnknownRunningAndClosedTaskIDs(t *testing.T) {
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
		result, err := manager.ExecuteTask(context.Background(), request)
		if err != nil {
			t.Fatalf("resume returned outer error: %v", err)
		}
		if request.TaskID == "unknown" {
			if result.State != TaskStateError || result.ErrorCode != TaskErrorNotFound {
				t.Fatalf("unknown task resume result = %#v", result)
			}
		} else if result.State != TaskStateError || result.ErrorCode != TaskErrorRunning {
			t.Fatalf("running task resume result = %#v", result)
		}
	}
	if _, err := manager.CloseSubagent(context.Background(), "mainAgent", record.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := manager.ExecuteTask(context.Background(), TaskRequest{
		MainAgentSessionID: "mainAgent", MainAgentTurnID: "resume", TaskID: record.ID, Prompt: "continue",
	})
	if err != nil || closed.State != TaskStateError || closed.ErrorCode != TaskErrorClosed {
		t.Fatalf("closed task resume result = %#v, err = %v", closed, err)
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
		seenNames := make(map[string]struct{})
		for index := 0; index < 6; index++ {
			record, err := manager.Start(context.Background(), "mainAgent", fmt.Sprintf("turn-%d", index), "researcher", "parallel", "")
			if err != nil {
				t.Fatalf("start task %d beyond the removed quota: %v", index, err)
			}
			if record.DisplayName == "" {
				t.Fatalf("task %d has no display name", index)
			}
			if _, duplicate := seenNames[record.DisplayName]; duplicate {
				t.Fatalf("duplicate display name %q", record.DisplayName)
			}
			seenNames[record.DisplayName] = struct{}{}
		}
		subagents, err := manager.List(context.Background(), "mainAgent", false)
		if err != nil || len(subagents) != 6 {
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
	awaitSubagentStatus(t, manager, record.ID, "")
	retained, err := manager.getOwned(context.Background(), "mainAgent", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.LastSubagentTurnID != record.CurrentSubagentTurnID || !strings.Contains(retained.LastResultError, providerErr.Error()) {
		t.Fatalf("retained failure = %#v", retained)
	}
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
	awaitSubagentStatus(t, manager, record.ID, "")

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
	awaitSubagentStatus(t, manager, record.ID, "")
	closed, err := manager.CloseSubagent(context.Background(), "mainAgent-a", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Subagent.Status != storage.SubagentStatusClosed {
		t.Fatalf("closed record = %#v", closed)
	}
	closedAgain, err := manager.CloseSubagent(context.Background(), "mainAgent-a", record.ID)
	if err != nil || closedAgain.Subagent.Status != storage.SubagentStatusClosed {
		t.Fatalf("idempotent close = %#v, %v", closedAgain, err)
	}
	if _, err := manager.Send(context.Background(), "mainAgent-a", record.ID, "again"); !errors.Is(err, storage.ErrSubagentClosed) {
		t.Fatalf("Send closed error = %v", err)
	}
	// Closing preserves the subagent transcript for later nested-chat rendering.
	if _, err := manager.Read(context.Background(), "mainAgent-a", record.ID, ""); err != nil {
		t.Fatalf("Read closed history error = %v", err)
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
		awaitSubagentStatus(t, manager, record.ID, "")
		finished, err := manager.getOwned(context.Background(), "mainAgent", record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CloseSubagent(context.Background(), "mainAgent", record.ID); err != nil {
			t.Fatal(err)
		}
		retained, err := manager.Run(context.Background(), "mainAgent", record.ID, finished.LastSubagentTurnID)
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
			!closed.Interrupted {
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

func newTestSubagentManager(t *testing.T, model agentruntime.Model, _ int) *subagentManager {
	return newTestSubagentManagerWithStorage(t, model, 0, inmemory.NewMessageStorage())
}

func newTestSubagentManagerWithStorage(t *testing.T, model agentruntime.Model, _ int, messages storage.MessageStorage) *subagentManager {
	return newTestSubagentManagerWithStores(t, model, messages, inmemory.NewSubagentStorage())
}

func newTestSubagentManagerWithStores(t *testing.T, model agentruntime.Model, messages storage.MessageStorage, subagents storage.SubagentStorage) *subagentManager {
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
		messages: messages, permissions: permissions, subagents: subagents,
		permissionMode: mainAgent.PermissionMode(), permissionPolicy: permission.Policy{Mode: mainAgent.PermissionMode()},
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

type taskFinalModel struct{ content string }

func (model taskFinalModel) Start(context.Context, agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	return scriptedStream{result: provider.StreamResult{Content: model.content, Finished: true}}, nil
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
