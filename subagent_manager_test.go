package agentcli

import (
	"context"
	"errors"
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
		record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "first", "label")
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
		t.Fatal("Start waited for child completion")
	}
	if record.Status != storage.SubagentStatusRunning || record.SessionID == "parent" || record.CurrentTurnID == "" {
		t.Fatalf("start record = %#v", record)
	}
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}

	queued, err := manager.Send(context.Background(), "parent", record.ID, "second")
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
		t.Fatalf("child requests = %#v", requests)
	}
}

func TestSubagentManagerDeduplicatesParentTurnMessages(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 2)
	defer manager.Close()

	record, err := manager.Start(context.Background(), "parent", "turn-1", "researcher", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	exact, err := manager.SendFromParentTurn(context.Background(), "parent", "turn-1", record.ID, " first \r\n")
	if err != nil {
		t.Fatal(err)
	}
	if exact.Action != toolexecution.SubagentSendDuplicate || exact.Accepted || !exact.Deduplicated || len(exact.Subagent.Pending) != 0 || len(exact.IdempotencyKey) != 64 {
		t.Fatalf("exact duplicate = %#v", exact)
	}
	changed, err := manager.SendFromParentTurn(context.Background(), "parent", "turn-1", record.ID, "different wording")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Action != toolexecution.SubagentSendAlreadySent || changed.Accepted || changed.Deduplicated || len(changed.Subagent.Pending) != 0 {
		t.Fatalf("changed repeat = %#v", changed)
	}
	queued, err := manager.SendFromParentTurn(context.Background(), "parent", "turn-2", record.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if queued.Action != toolexecution.SubagentSendQueued || !queued.Accepted || len(queued.Subagent.Pending) != 1 {
		t.Fatalf("next parent turn = %#v", queued)
	}
	retry, err := manager.SendFromParentTurn(context.Background(), "parent", "turn-2", record.ID, " second ")
	if err != nil {
		t.Fatal(err)
	}
	if retry.Action != toolexecution.SubagentSendDuplicate || retry.Accepted || !retry.Deduplicated || len(retry.Subagent.Pending) != 1 || retry.IdempotencyKey != queued.IdempotencyKey {
		t.Fatalf("queued retry = %#v", retry)
	}

	model.releases <- struct{}{}
	if err := model.waitStarts(2); err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	if got := model.Requests(); len(got) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(got))
	}
}

func TestSubagentManagerIdleSendWaitsForLatestCallbackObservation(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 1)}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	callbacks := manager.subscribeCallbacks(context.Background())
	record, err := manager.Start(context.Background(), "parent", "start-turn", "researcher", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	var callback SubagentCallback
	select {
	case callback = <-callbacks:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for child callback")
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	if callback.Status != SubagentCallbackIncomplete {
		t.Fatalf("callback status = %q, want incomplete", callback.Status)
	}
	if _, err := manager.Send(context.Background(), "parent", record.ID, "follow up"); !errors.Is(err, storage.ErrSubagentCallbackPending) {
		t.Fatalf("direct send before callback observation error = %v", err)
	}
	if _, err := manager.SendFromParentTurn(context.Background(), "parent", "early-turn", record.ID, "follow up"); !errors.Is(err, storage.ErrSubagentCallbackPending) {
		t.Fatalf("model send before callback observation error = %v", err)
	}
	observeTestSubagentCallback(t, manager, callback)
	sent, err := manager.SendFromParentTurn(context.Background(), "parent", "callback-turn", record.ID, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if sent.Action != toolexecution.SubagentSendStarted || !sent.Accepted || sent.Subagent.Status != storage.SubagentStatusRunning {
		t.Fatalf("send after callback observation = %#v", sent)
	}
}

func TestSubagentManagerStartOrReuseRoutesConversationalFollowUps(t *testing.T) {
	t.Run("one open child is reused", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 3)
		defer manager.Close()
		first, err := manager.Start(context.Background(), "parent", "turn-1", "researcher", "first", "")
		if err != nil {
			t.Fatal(err)
		}
		routed, err := manager.StartOrReuse(context.Background(), "parent", "turn-2", "researcher", "talk more", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if routed.Action != toolexecution.SubagentStartReused || routed.DispatchAction != toolexecution.SubagentSendQueued || routed.Subagent.ID != first.ID || len(routed.Subagent.Pending) != 1 || routed.Subagent.Pending[0].Content != "talk more" {
			t.Fatalf("routed result = %#v", routed)
		}
		children, err := manager.List(context.Background(), "parent", false)
		if err != nil || len(children) != 1 {
			t.Fatalf("children = %#v, %v", children, err)
		}
	})

	t.Run("many open children require user selection", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 3)
		defer manager.Close()
		first, err := manager.Start(context.Background(), "parent", "turn-1", "researcher", "first", "")
		if err != nil {
			t.Fatal(err)
		}
		second, err := manager.Start(context.Background(), "parent", "turn-2", "researcher", "second", "")
		if err != nil {
			t.Fatal(err)
		}
		if first.DisplayName == "" || second.DisplayName == "" || first.DisplayName == second.DisplayName {
			t.Fatalf("friendly names = %q and %q", first.DisplayName, second.DisplayName)
		}
		routed, err := manager.StartOrReuse(context.Background(), "parent", "turn-3", "researcher", "talk more", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if routed.Action != toolexecution.SubagentStartSelectionRequired || len(routed.Candidates) != 2 || routed.Subagent.ID != "" {
			t.Fatalf("routed result = %#v", routed)
		}
		children, err := manager.List(context.Background(), "parent", false)
		if err != nil || len(children) != 2 {
			t.Fatalf("children = %#v, %v", children, err)
		}
	})

	t.Run("explicit new instance always creates", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 3)
		defer manager.Close()
		first, err := manager.Start(context.Background(), "parent", "turn-1", "researcher", "first", "")
		if err != nil {
			t.Fatal(err)
		}
		routed, err := manager.StartOrReuse(context.Background(), "parent", "turn-2", "researcher", "parallel", "", true)
		if err != nil {
			t.Fatal(err)
		}
		if routed.Action != toolexecution.SubagentStartCreated || routed.Subagent.ID == first.ID || routed.Subagent.DisplayName == first.DisplayName {
			t.Fatalf("routed result = %#v, first = %#v", routed, first)
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
		record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "visible", "")
		returned <- result{record: record, err: err}
	}()
	select {
	case <-messages.entered:
	case <-time.After(time.Second):
		t.Fatal("child did not attempt its initial append")
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
	read, err := manager.Read(context.Background(), "parent", outcome.record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if read.FinalAnswer != nil || read.NextMessageID == "" {
		t.Fatalf("Read immediately after Start = %#v, want no final answer and an advanced cursor", read)
	}
}

func TestSubagentManagerRetainsLastTurnFailure(t *testing.T) {
	providerErr := errors.New("provider failed before answering")
	manager := newTestSubagentManager(t, subagentFailModel{err: providerErr}, 1)
	defer manager.Close()

	record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "inspect project", "")
	if err != nil {
		t.Fatal(err)
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	idle, err := manager.getOwned(context.Background(), "parent", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if idle.LastTurnID != record.CurrentTurnID || !strings.Contains(idle.LastTurnError, providerErr.Error()) {
		t.Fatalf("idle failure = %#v", idle)
	}
}

func TestSubagentManagerPublishesCompactSuccessAndFailureCallbacks(t *testing.T) {
	t.Run("success includes final assistant answer", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 1)
		defer manager.Close()
		callbacks := manager.subscribeCallbacks(context.Background())
		record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "work", "")
		if err != nil {
			t.Fatal(err)
		}
		model.releases <- struct{}{}
		select {
		case callback := <-callbacks:
			if callback.SubagentID != record.ID || callback.DisplayName != record.DisplayName || callback.Status != SubagentCallbackIncomplete || callback.Error != "" || callback.FinalAnswer == nil || callback.FinalAnswer.Content != "done" || callback.NextMessageID == "" {
				t.Fatalf("callback = %#v", callback)
			}
			message := callback.RuntimeMessage()
			for _, expected := range []string{"authoritative outcome", "dispatch acknowledgement", "display_name", "send one focused follow-up", "Never close a running or incomplete child", "Never poll list_subagents or subagent_status", "unfinished children will callback automatically", "Never reveal secret values", "callback to be consumed"} {
				if !strings.Contains(message.Content, expected) {
					t.Fatalf("callback instruction missing %q: %s", expected, message.Content)
				}
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for success callback")
		}
	})

	t.Run("failure includes terminal error", func(t *testing.T) {
		manager := newTestSubagentManager(t, subagentFailModel{err: errors.New("provider unavailable")}, 1)
		defer manager.Close()
		callbacks := manager.subscribeCallbacks(context.Background())
		record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "work", "")
		if err != nil {
			t.Fatal(err)
		}
		select {
		case callback := <-callbacks:
			if callback.SubagentID != record.ID || callback.Status != SubagentCallbackFailed || !strings.Contains(callback.Error, "provider unavailable") || callback.FinalAnswer != nil {
				t.Fatalf("callback = %#v", callback)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for failure callback")
		}
	})
}

func TestSubagentManagerReadDefaultsToObservedCursorAndFinalAnswerOnly(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)

	first, err := manager.Read(context.Background(), "parent", record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.FinalAnswer == nil || first.FinalAnswer.Content != "done" || first.NextMessageID == "" {
		t.Fatalf("first read = %#v", first)
	}
	second, err := manager.Read(context.Background(), "parent", record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.FinalAnswer != nil || second.NextMessageID != first.NextMessageID {
		t.Fatalf("second read replayed output: %#v", second)
	}
}

func TestSubagentManagerReadOwnershipWaitAndClose(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "parent-a", "parent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Read(context.Background(), "parent-b", record.ID, ""); !errors.Is(err, storage.ErrSubagentNotFound) {
		t.Fatalf("cross-parent Read error = %v", err)
	}
	read, err := manager.Read(context.Background(), "parent-a", record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if read.FinalAnswer != nil || read.NextMessageID == "" || read.Subagent.ObservedMessageID != read.NextMessageID {
		t.Fatalf("read result = %#v", read)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Wait(canceled, "parent-a", []string{record.ID}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait cancellation error = %v", err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	callback := markTestSubagentCompleted(t, manager, record.ID)
	if _, err := manager.CloseSubagent(context.Background(), "parent-a", record.ID); !errors.Is(err, storage.ErrSubagentCallbackPending) {
		t.Fatalf("close before callback observation error = %v", err)
	}
	observeTestSubagentCallback(t, manager, callback)
	closed, err := manager.CloseSubagent(context.Background(), "parent-a", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != storage.SubagentStatusClosed {
		t.Fatalf("closed record = %#v", closed)
	}
	if _, err := manager.Send(context.Background(), "parent-a", record.ID, "again"); !errors.Is(err, storage.ErrSubagentClosed) {
		t.Fatalf("Send closed error = %v", err)
	}
	// Closing preserves the child transcript for later nested-chat rendering.
	if _, err := manager.Read(context.Background(), "parent-a", record.ID, ""); err != nil {
		t.Fatalf("Read closed history error = %v", err)
	}
}

func TestSubagentManagerCloseRetainsRunsAfterReleasingChild(t *testing.T) {
	t.Run("completed run remains available for SSE backfill", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 1)
		defer manager.Close()
		record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "complete", "")
		if err != nil {
			t.Fatal(err)
		}
		run, err := manager.Run(context.Background(), "parent", record.ID, record.CurrentTurnID)
		if err != nil {
			t.Fatal(err)
		}
		model.releases <- struct{}{}
		awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
		observeTestSubagentCallback(t, manager, markTestSubagentCompleted(t, manager, record.ID))
		idle, err := manager.getOwned(context.Background(), "parent", record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CloseSubagent(context.Background(), "parent", record.ID); err != nil {
			t.Fatal(err)
		}
		retained, err := manager.Run(context.Background(), "parent", record.ID, idle.LastTurnID)
		if err != nil {
			t.Fatal(err)
		}
		if retained != run || len(retained.Events()) == 0 {
			t.Fatalf("retained completed run = %#v, want original event history", retained)
		}
	})

	t.Run("closing active child is rejected and interrupt preserves its run", func(t *testing.T) {
		model := &subagentGateModel{releases: make(chan struct{})}
		manager := newTestSubagentManager(t, model, 1)
		defer manager.Close()
		record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "active", "")
		if err != nil {
			t.Fatal(err)
		}
		run, err := manager.Run(context.Background(), "parent", record.ID, record.CurrentTurnID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CloseSubagent(context.Background(), "parent", record.ID); !errors.Is(err, storage.ErrSubagentRunning) {
			t.Fatalf("CloseSubagent running error = %v", err)
		}
		if run.Done() {
			t.Fatal("rejected close interrupted the active run")
		}
		if err := manager.Interrupt(context.Background(), "parent", record.ID, "stop before close"); err != nil {
			t.Fatal(err)
		}
		waitRun(t, run)
		awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
		failed, err := manager.getOwned(context.Background(), "parent", record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if failed.LastTurnOutcome != storage.SubagentTurnFailed {
			t.Fatalf("interrupted child outcome = %q, want failed", failed.LastTurnOutcome)
		}
		if _, err := manager.CloseSubagent(context.Background(), "parent", record.ID); !errors.Is(err, storage.ErrSubagentCallbackPending) {
			t.Fatalf("close failed child before callback observation error = %v", err)
		}
		messages, err := manager.parent.ListMessages(context.Background(), failed.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		observeTestSubagentCallback(t, manager, callbackFromMessages(failed, messages))
		if _, err := manager.CloseSubagent(context.Background(), "parent", record.ID); err != nil {
			t.Fatalf("close observed failed child: %v", err)
		}
		retained, err := manager.Run(context.Background(), "parent", record.ID, record.CurrentTurnID)
		if err != nil {
			t.Fatal(err)
		}
		if retained != run || !retained.Done() || len(retained.Events()) == 0 {
			t.Fatalf("retained active run = %#v, want completed original", retained)
		}
	})
}

func TestSubagentManagerForceCloseInterruptsAndDropsQueuedWork(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	callbacks := manager.subscribeCallbacks(context.Background())

	record, err := manager.Start(context.Background(), "parent", "start-turn", "researcher", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.Run(context.Background(), "parent", record.ID, record.CurrentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := manager.Send(context.Background(), "parent", record.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Pending) != 1 {
		t.Fatalf("queued messages = %d, want 1", len(queued.Pending))
	}

	result, err := manager.ForceCloseSubagent(context.Background(), "parent", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Subagent.Status != storage.SubagentStatusClosed || result.PreviousStatus != storage.SubagentStatusRunning || result.PreviousOutcome != "" || result.DroppedMessages != 1 || !result.Interrupted {
		t.Fatalf("force close result = %#v", result)
	}
	if len(result.Subagent.Pending) != 0 || result.Subagent.ClosedAt == nil {
		t.Fatalf("closed child = %#v", result.Subagent)
	}
	if _, err := manager.Send(context.Background(), "parent", record.ID, "after close"); !errors.Is(err, storage.ErrSubagentClosed) {
		t.Fatalf("send after force close error = %v", err)
	}
	retained, err := manager.Run(context.Background(), "parent", record.ID, record.CurrentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, retained)
	if retained != run || !retained.Done() || len(retained.Events()) == 0 {
		t.Fatalf("retained force-closed run mismatch: same=%t done=%t events=%d", retained == run, retained.Done(), len(retained.Events()))
	}
	messages, err := manager.parent.ListMessages(context.Background(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 || messages[0].Content != "first" {
		t.Fatalf("retained child transcript = %#v", messages)
	}
	select {
	case callback := <-callbacks:
		t.Fatalf("force-closed child published callback: %#v", callback)
	case <-time.After(25 * time.Millisecond):
	}
}

func newTestSubagentManager(t *testing.T, model agentruntime.Model, maximum int) *subagentManager {
	return newTestSubagentManagerWithStorage(t, model, maximum, inmemory.NewMessageStorage())
}

func markTestSubagentCompleted(t *testing.T, manager *subagentManager, id string) SubagentCallback {
	t.Helper()
	record, found, err := manager.store.Get(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("get child for completion = (%#v, %v, %v)", record, found, err)
	}
	completed, err := manager.store.Update(context.Background(), id, record.Version, storage.SubagentUpdate{
		Status:           record.Status,
		CurrentTurnID:    record.CurrentTurnID,
		LastTurnID:       record.LastTurnID,
		LastTurnOutcome:  storage.SubagentTurnCompleted,
		LastTurnSummary:  "test work completed",
		LastTurnNextStep: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := manager.parent.ListMessages(context.Background(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	return callbackFromMessages(completed, messages)
}

func observeTestSubagentCallback(t *testing.T, manager *subagentManager, callback SubagentCallback) {
	t.Helper()
	if err := manager.observeCallback(context.Background(), callback); err != nil {
		t.Fatal(err)
	}
}

func newTestSubagentManagerWithStorage(t *testing.T, model agentruntime.Model, maximum int, messages storage.MessageStorage) *subagentManager {
	t.Helper()
	permissions := inmemory.NewPermissionStorage()
	parent, err := New(context.Background(), WithModel(&scriptedModel{}), WithMessageStorage(messages), WithPermissionStorage(permissions))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	manager, err := newSubagentManager(parent, config{
		project: &Project{subagents: map[string]SubagentDefinition{
			"researcher": {Name: "researcher", Description: "Research", Provider: "test", Model: "test", Instructions: "be useful"},
		}},
		messages: messages, permissions: permissions, subagents: inmemory.NewSubagentStorage(),
		maxSubagents: maximum, permissionMode: parent.PermissionMode(), permissionPolicy: permission.Policy{Mode: parent.PermissionMode()},
		toolWorkers: defaultToolWorkers, channelBuffer: defaultChannelBuffer, skillReload: DefaultSkillReloadPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.childFactory = func(SubagentDefinition) (*Agent, error) {
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
	return errors.New("child provider did not start")
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
