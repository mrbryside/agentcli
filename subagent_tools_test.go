package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestSubagentToolsValidateInvocationAndOwnership(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 2)
	defer manager.Close()
	bridge := newTestSubagentToolBridge(manager)

	if _, err := callSubagentTool(bridge, StartSubagentToolName, context.Background(), json.RawMessage(`{"name":"researcher","message":"work"}`)); err == nil {
		t.Fatal("start without invocation context error = nil")
	}
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent-a", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
	output, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"work"}`))
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID           string                 `json:"subagent_id"`
		Status       storage.SubagentStatus `json:"status"`
		Asynchronous bool                   `json:"asynchronous"`
		FinishTurn   bool                   `json:"finish_turn"`
		TurnBehavior string                 `json:"turn_behavior"`
	}
	if err := json.Unmarshal(output, &started); err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.Status != storage.SubagentStatusRunning || !started.Asynchronous || !started.FinishTurn || started.TurnBehavior != "end_turn" {
		t.Fatalf("start result = %s", output)
	}
	statusCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent-a", TurnID: "turn", CallID: "status", ToolName: SubagentStatusToolName})
	statusJSON, err := callSubagentTool(bridge, SubagentStatusToolName, statusCtx, json.RawMessage(`{"subagent_id":"`+started.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var status toolexecution.SubagentStatusResult
	if err := json.Unmarshal(statusJSON, &status); err != nil {
		t.Fatal(err)
	}
	if status.Subagent.Status != storage.SubagentStatusRunning || status.ResultReady || !strings.Contains(status.ActivitySummary, "Working on") || strings.Contains(string(statusJSON), `"messages"`) {
		t.Fatalf("lightweight status result = %s", statusJSON)
	}
	if status.Action != "snapshot" || !strings.Contains(status.Instruction, "Do not call subagent_status again") {
		t.Fatalf("initial status contract = %s", statusJSON)
	}
	wrongStatus := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent-b", TurnID: "turn", CallID: "status", ToolName: SubagentStatusToolName})
	if _, err := callSubagentTool(bridge, SubagentStatusToolName, wrongStatus, json.RawMessage(`{"subagent_id":"`+started.ID+`"}`)); !errors.Is(err, storage.ErrSubagentNotFound) {
		t.Fatalf("cross-parent status error = %v", err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, started.ID, storage.SubagentStatusIdle)
	cachedJSON, err := callSubagentTool(bridge, SubagentStatusToolName, statusCtx, json.RawMessage(`{"subagent_id":"`+started.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(cachedJSON, &status); err != nil {
		t.Fatal(err)
	}
	if status.Action != "already_checked" || status.Subagent.Status != storage.SubagentStatusRunning || !strings.Contains(status.Instruction, "cached snapshot") {
		t.Fatalf("repeated status was not cached = %s", cachedJSON)
	}
	completedCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent-a", TurnID: "next-turn", CallID: "status-next", ToolName: SubagentStatusToolName})
	completedJSON, err := callSubagentTool(bridge, SubagentStatusToolName, completedCtx, json.RawMessage(`{"subagent_id":"`+started.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(completedJSON, &status); err != nil {
		t.Fatal(err)
	}
	if status.Subagent.Status != storage.SubagentStatusIdle || status.ResultReady || status.Subagent.LastTurnOutcome != storage.SubagentTurnIncomplete || !strings.Contains(status.ActivitySummary, "Incomplete") {
		t.Fatalf("incomplete status result = %s", completedJSON)
	}
}

func TestCloseSubagentToolRejectsRunningChildUntilItsCallbackCanFinish(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	bridge := newTestSubagentToolBridge(manager)
	startCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "start-turn", CallID: "start", ToolName: StartSubagentToolName})
	startedJSON, err := callSubagentTool(bridge, StartSubagentToolName, startCtx, json.RawMessage(`{"name":"researcher","message":"work"}`))
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID string `json:"subagent_id"`
	}
	if err := json.Unmarshal(startedJSON, &started); err != nil {
		t.Fatal(err)
	}
	closeCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "start-turn", CallID: "close", ToolName: CloseSubagentToolName})
	if _, err := callSubagentTool(bridge, CloseSubagentToolName, closeCtx, json.RawMessage(`{"subagent_id":"`+started.ID+`"}`)); !errors.Is(err, storage.ErrSubagentRunning) {
		t.Fatalf("close running child error = %v", err)
	}
	record, found, err := manager.store.Get(context.Background(), started.ID)
	if err != nil || !found || record.Status != storage.SubagentStatusRunning {
		t.Fatalf("child after rejected close = (%#v, %v, %v)", record, found, err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, started.ID, storage.SubagentStatusIdle)
	closeCtx = toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "callback-turn", CallID: "close", ToolName: CloseSubagentToolName})
	if _, err := callSubagentTool(bridge, CloseSubagentToolName, closeCtx, json.RawMessage(`{"subagent_id":"`+started.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestSubagentToolFactoriesAreCompleteAndReserved(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	tools := bridge.Tools()
	if len(tools) != 5 {
		t.Fatalf("tool count = %d, want 5", len(tools))
	}
	seen := make(map[string]bool)
	for _, tool := range tools {
		if !isSubagentToolName(tool.Definition.Name) {
			t.Fatalf("unreserved tool %q", tool.Definition.Name)
		}
		if !json.Valid(tool.Definition.InputSchema) {
			t.Fatalf("invalid schema for %q", tool.Definition.Name)
		}
		seen[tool.Definition.Name] = true
	}
	for name := range subagentToolNames {
		if !seen[name] {
			t.Fatalf("missing reserved tool %q", name)
		}
	}
}

func TestStartSubagentToolReusesOneChildAndRequiresSelectionForMany(t *testing.T) {
	t.Run("one child", func(t *testing.T) {
		manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 3)
		defer manager.Close()
		bridge := newTestSubagentToolBridge(manager)
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
		firstJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"first"}`))
		if err != nil {
			t.Fatal(err)
		}
		var first struct {
			ID          string                            `json:"subagent_id"`
			DisplayName string                            `json:"display_name"`
			Action      toolexecution.SubagentStartAction `json:"action"`
		}
		if err := json.Unmarshal(firstJSON, &first); err != nil {
			t.Fatal(err)
		}
		secondJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"talk more"}`))
		if err != nil {
			t.Fatal(err)
		}
		var second struct {
			ID             string                            `json:"subagent_id"`
			Action         toolexecution.SubagentStartAction `json:"action"`
			DispatchAction toolexecution.SubagentSendAction  `json:"dispatch_action"`
			Reused         bool                              `json:"reused"`
		}
		if err := json.Unmarshal(secondJSON, &second); err != nil {
			t.Fatal(err)
		}
		if first.ID == "" || first.DisplayName == "" || first.Action != toolexecution.SubagentStartCreated || second.ID != first.ID || second.Action != toolexecution.SubagentStartReused || second.DispatchAction != toolexecution.SubagentSendAlreadySent || !second.Reused {
			t.Fatalf("first = %s, second = %s", firstJSON, secondJSON)
		}
	})

	t.Run("many children", func(t *testing.T) {
		manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 3)
		defer manager.Close()
		bridge := newTestSubagentToolBridge(manager)
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
		for _, message := range []string{"first", "second"} {
			arguments := json.RawMessage(`{"name":"researcher","message":"` + message + `","new_instance":true}`)
			if _, err := callSubagentTool(bridge, StartSubagentToolName, ctx, arguments); err != nil {
				t.Fatal(err)
			}
		}
		selectionJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"talk more"}`))
		if err != nil {
			t.Fatal(err)
		}
		var selection struct {
			Action     toolexecution.SubagentStartAction   `json:"action"`
			Candidates []toolexecution.SubagentToolSummary `json:"candidates"`
			FinishTurn bool                                `json:"finish_turn"`
			Behavior   string                              `json:"turn_behavior"`
			NextAction string                              `json:"next_action"`
		}
		if err := json.Unmarshal(selectionJSON, &selection); err != nil {
			t.Fatal(err)
		}
		if selection.Action != toolexecution.SubagentStartSelectionRequired || selection.FinishTurn || selection.Behavior != "continue_turn" || len(selection.Candidates) != 2 || selection.Candidates[0].DisplayName == "" || selection.Candidates[1].DisplayName == "" || selection.Candidates[0].DisplayName == selection.Candidates[1].DisplayName || !strings.Contains(selection.NextAction, "Ask the user") {
			t.Fatalf("selection = %s", selectionJSON)
		}
	})
}

func TestSendSubagentMessageToolDoesNotMultiplyOneParentTurn(t *testing.T) {
	manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 2)
	defer manager.Close()
	bridge := newTestSubagentToolBridge(manager)
	startCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "turn-1", CallID: "start", ToolName: StartSubagentToolName})
	startedJSON, err := callSubagentTool(bridge, StartSubagentToolName, startCtx, json.RawMessage(`{"name":"researcher","message":"work"}`))
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID string `json:"subagent_id"`
	}
	if err := json.Unmarshal(startedJSON, &started); err != nil {
		t.Fatal(err)
	}

	type sendResult struct {
		Action       toolexecution.SubagentSendAction `json:"action"`
		Accepted     bool                             `json:"accepted"`
		Deduplicated bool                             `json:"deduplicated"`
		Subagent     struct {
			QueuedMessages int `json:"queued_messages"`
		} `json:"subagent"`
		FinishTurn  bool   `json:"finish_turn"`
		Behavior    string `json:"turn_behavior"`
		Instruction string `json:"instruction"`
	}
	send := func(turnID, callID, message string) sendResult {
		t.Helper()
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: turnID, CallID: callID, ToolName: SendSubagentMessageToolName})
		arguments, err := json.Marshal(map[string]string{"subagent_id": started.ID, "message": message})
		if err != nil {
			t.Fatal(err)
		}
		output, err := callSubagentTool(bridge, SendSubagentMessageToolName, ctx, arguments)
		if err != nil {
			t.Fatal(err)
		}
		var result sendResult
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if duplicate := send("turn-1", "duplicate", " work "); duplicate.Action != toolexecution.SubagentSendDuplicate || duplicate.Accepted || !duplicate.Deduplicated || duplicate.Subagent.QueuedMessages != 0 {
		t.Fatalf("duplicate = %#v", duplicate)
	}
	if changed := send("turn-1", "changed", "wait for the result"); changed.Action != toolexecution.SubagentSendAlreadySent || changed.Accepted || changed.Deduplicated || changed.Subagent.QueuedMessages != 0 || !strings.Contains(changed.Instruction, "Nothing new was queued") {
		t.Fatalf("changed repeat = %#v", changed)
	}
	if queued := send("turn-2", "accepted", "next task"); queued.Action != toolexecution.SubagentSendQueued || !queued.Accepted || queued.Deduplicated || queued.Subagent.QueuedMessages != 1 || !queued.FinishTurn || queued.Behavior != "end_turn" {
		t.Fatalf("next turn = %#v", queued)
	}
}

func TestReadSubagentRecoveryAPIReportsLastTurnFailure(t *testing.T) {
	manager := newTestSubagentManager(t, subagentFailModel{err: errors.New("provider unavailable")}, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "inspect project", "")
	if err != nil {
		t.Fatal(err)
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	result, err := manager.Read(context.Background(), "parent", record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Subagent.LastTurnID == "" || !strings.Contains(result.Subagent.LastTurnError, "provider unavailable") {
		t.Fatalf("recovery result = %#v", result)
	}
}

func TestReadAndWaitSubagentAreNotExposedToTheModel(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	for _, name := range []string{"read_subagent", "wait_subagent"} {
		if _, err := callSubagentTool(bridge, name, context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("%s availability error = %v", name, err)
		}
	}
}

func TestStartSubagentRejectsSynchronousExecutionOption(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	startCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "parent", TurnID: "parent-turn", CallID: "start", ToolName: StartSubagentToolName,
	})
	if _, err := callSubagentTool(bridge, StartSubagentToolName, startCtx, json.RawMessage(`{"name":"researcher","message":"summarize README","background":false}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("synchronous start error = %v", err)
	}
}

func newTestSubagentToolBridge(manager *subagentManager) *toolexecution.SubagentToolBridge {
	bridge := toolexecution.NewSubagentToolBridge()
	bridge.Bind(manager)
	return bridge
}

func callSubagentTool(bridge *toolexecution.SubagentToolBridge, name string, ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	for _, tool := range bridge.Tools() {
		if tool.Definition.Name == name {
			return tool.Handler(ctx, arguments)
		}
	}
	return nil, errors.New("subagent built-in is unavailable")
}
