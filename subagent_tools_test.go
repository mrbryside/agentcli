package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
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
		Callback     string                 `json:"callback_action"`
		MustWait     bool                   `json:"must_wait_for_callback"`
		NextAction   string                 `json:"next_action"`
	}
	if err := json.Unmarshal(output, &started); err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.Status != storage.SubagentStatusRunning || !started.Asynchronous || started.Callback != "automatic" || !started.MustWait || started.NextAction != "Accepted. The result will arrive automatically later." || strings.Contains(string(output), `"finish_turn"`) || strings.Contains(string(output), `"prohibited_actions"`) || strings.Contains(string(output), `"turn_behavior"`) {
		t.Fatalf("start result = %s", output)
	}
	if strings.Contains(string(output), "close_subagent") {
		t.Fatalf("removed destructive tool appears in start acknowledgement: %s", output)
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

func TestSubagentToolFactoriesAreCompleteAndReserved(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	tools := bridge.Tools()
	if len(tools) != 4 {
		t.Fatalf("tool count = %d, want 4", len(tools))
	}
	seen := make(map[string]bool)
	for _, tool := range tools {
		if !isSubagentToolName(tool.Definition.Name) {
			t.Fatalf("unreserved tool %q", tool.Definition.Name)
		}
		encoded, err := json.Marshal(tool.Definition.InputSchema)
		if err != nil || !json.Valid(encoded) {
			t.Fatalf("invalid schema for %q: %v", tool.Definition.Name, err)
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
			ID           string                            `json:"subagent_id"`
			DisplayName  string                            `json:"display_name"`
			Action       toolexecution.SubagentStartAction `json:"action"`
			Accepted     bool                              `json:"accepted"`
			Deduplicated bool                              `json:"deduplicated"`
		}
		if err := json.Unmarshal(firstJSON, &first); err != nil {
			t.Fatal(err)
		}
		duplicateJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":" first "}`))
		if err != nil {
			t.Fatal(err)
		}
		var duplicate struct {
			Action         toolexecution.SubagentStartAction `json:"action"`
			DispatchAction toolexecution.SubagentSendAction  `json:"dispatch_action"`
			Accepted       bool                              `json:"accepted"`
			Deduplicated   bool                              `json:"deduplicated"`
		}
		if err := json.Unmarshal(duplicateJSON, &duplicate); err != nil {
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
			Accepted       bool                              `json:"accepted"`
			Deduplicated   bool                              `json:"deduplicated"`
		}
		if err := json.Unmarshal(secondJSON, &second); err != nil {
			t.Fatal(err)
		}
		nextTurnCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "turn-2", CallID: "call-2", ToolName: StartSubagentToolName})
		acceptedJSON, err := callSubagentTool(bridge, StartSubagentToolName, nextTurnCtx, json.RawMessage(`{"name":"researcher","message":"talk more"}`))
		if err != nil {
			t.Fatal(err)
		}
		var accepted struct {
			ID             string                            `json:"subagent_id"`
			Action         toolexecution.SubagentStartAction `json:"action"`
			DispatchAction toolexecution.SubagentSendAction  `json:"dispatch_action"`
			Accepted       bool                              `json:"accepted"`
			Deduplicated   bool                              `json:"deduplicated"`
		}
		if err := json.Unmarshal(acceptedJSON, &accepted); err != nil {
			t.Fatal(err)
		}
		if first.ID == "" || first.DisplayName == "" || first.Action != toolexecution.SubagentStartCreated || !first.Accepted || first.Deduplicated ||
			duplicate.Action != toolexecution.SubagentStartReused || duplicate.DispatchAction != toolexecution.SubagentSendDuplicate || duplicate.Accepted || !duplicate.Deduplicated ||
			second.ID != first.ID || second.Action != toolexecution.SubagentStartReused || second.DispatchAction != toolexecution.SubagentSendAlreadySent || !second.Reused || second.Accepted || second.Deduplicated ||
			accepted.ID != first.ID || accepted.Action != toolexecution.SubagentStartReused || accepted.DispatchAction != toolexecution.SubagentSendQueued || !accepted.Accepted || accepted.Deduplicated {
			t.Fatalf("first = %s, duplicate = %s, second = %s, accepted = %s", firstJSON, duplicateJSON, secondJSON, acceptedJSON)
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
			Action       toolexecution.SubagentStartAction   `json:"action"`
			Candidates   []toolexecution.SubagentToolSummary `json:"candidates"`
			Behavior     string                              `json:"turn_behavior"`
			NextAction   string                              `json:"next_action"`
			Accepted     bool                                `json:"accepted"`
			Deduplicated bool                                `json:"deduplicated"`
			Callback     string                              `json:"callback_action"`
			MustWait     bool                                `json:"must_wait_for_callback"`
		}
		if err := json.Unmarshal(selectionJSON, &selection); err != nil {
			t.Fatal(err)
		}
		if selection.Action != toolexecution.SubagentStartSelectionRequired || selection.Accepted || selection.Deduplicated || selection.Callback != "none" || selection.MustWait || selection.Behavior != "continue_turn" || strings.Contains(string(selectionJSON), `"finish_turn"`) || len(selection.Candidates) != 2 || selection.Candidates[0].DisplayName == "" || selection.Candidates[1].DisplayName == "" || selection.Candidates[0].DisplayName == selection.Candidates[1].DisplayName || !strings.Contains(selection.NextAction, "Ask the user") {
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
		Instruction string `json:"instruction"`
		Callback    string `json:"callback_action"`
		MustWait    bool   `json:"must_wait_for_callback"`
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
	if changed := send("turn-1", "changed", "wait for the result"); changed.Action != toolexecution.SubagentSendAlreadySent || changed.Accepted || changed.Deduplicated || changed.Subagent.QueuedMessages != 0 || changed.Instruction != "Not accepted. The existing result will arrive automatically later." {
		t.Fatalf("changed repeat = %#v", changed)
	}
	if queued := send("turn-2", "accepted", "next task"); queued.Action != toolexecution.SubagentSendQueued || !queued.Accepted || queued.Deduplicated || queued.Subagent.QueuedMessages != 1 || queued.Callback != "automatic" || !queued.MustWait || queued.Instruction != "Accepted. The result will arrive automatically later." {
		t.Fatalf("next turn = %#v", queued)
	}
}

func TestSendSubagentMessageToolReturnsCallbackPendingAsControlledResult(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 1)}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	callbacks := manager.subscribeCallbacks(context.Background())
	record, err := manager.Start(context.Background(), "parent", "start-turn", "researcher", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	select {
	case <-callbacks:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for child callback")
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	bridge := newTestSubagentToolBridge(manager)

	test := func(turnID string) {
		t.Helper()
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
			SessionID: "parent", TurnID: turnID, CallID: "send", ToolName: SendSubagentMessageToolName,
		})
		arguments, err := json.Marshal(map[string]any{"subagent_id": record.ID, "message": "too early"})
		if err != nil {
			t.Fatal(err)
		}
		output, err := callSubagentTool(bridge, SendSubagentMessageToolName, ctx, arguments)
		if err != nil {
			t.Fatalf("callback_pending returned tool error: %v", err)
		}
		var result struct {
			Action       toolexecution.SubagentSendAction `json:"action"`
			Accepted     bool                             `json:"accepted"`
			Callback     string                           `json:"callback_action"`
			MustWait     bool                             `json:"must_wait_for_callback"`
			TurnBehavior string                           `json:"turn_behavior"`
			Instruction  string                           `json:"instruction"`
		}
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		if result.Action != toolexecution.SubagentSendCallbackPending || result.Accepted || result.Callback != "automatic_existing" || !result.MustWait || strings.Contains(string(output), `"finish_turn"`) || result.Instruction != "Not accepted. The pending result will arrive automatically later." {
			t.Fatalf("callback_pending result = %s", output)
		}
	}

	test("callback-pending-turn")
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
	for _, name := range []string{"read_subagent", "wait_subagent", "close_subagent"} {
		if _, err := callSubagentTool(bridge, name, context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("%s availability error = %v", name, err)
		}
	}
}

func TestSubagentToolsRejectRemovedFinishTurnOption(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	tests := []struct {
		name      string
		toolName  string
		arguments json.RawMessage
	}{
		{"start background", StartSubagentToolName, json.RawMessage(`{"name":"researcher","message":"summarize README","background":false}`)},
		{"start finish", StartSubagentToolName, json.RawMessage(`{"name":"researcher","message":"summarize README","finish_turn":true}`)},
		{"send finish", SendSubagentMessageToolName, json.RawMessage(`{"subagent_id":"child","message":"continue","finish_turn":true}`)},
	}
	for _, test := range tests {
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
			SessionID: "parent", TurnID: "parent-turn", CallID: test.name, ToolName: test.toolName,
		})
		if _, err := callSubagentTool(bridge, test.toolName, ctx, test.arguments); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("%s removed option error = %v", test.name, err)
		}
	}
}

func newTestSubagentToolBridge(manager *subagentManager) *toolexecution.SubagentToolBridge {
	bridge := toolexecution.NewSubagentToolBridge()
	bridge.Bind(manager)
	return bridge
}

func appendParentUserMessage(t *testing.T, manager *subagentManager, sessionID, turnID, content string) {
	t.Helper()
	err := manager.parent.messages.Append(context.Background(), agentruntime.Message{
		ID:        "message-" + turnID,
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      agentruntime.MessageTypeUser,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func appendParentRuntimeMessage(t *testing.T, manager *subagentManager, sessionID, turnID, content string) {
	t.Helper()
	err := manager.parent.messages.Append(context.Background(), agentruntime.Message{
		ID:        "message-" + turnID,
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      agentruntime.MessageTypeRuntimeEvent,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func callSubagentTool(bridge *toolexecution.SubagentToolBridge, name string, ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	for _, tool := range bridge.Tools() {
		if tool.Definition.Name == name {
			return tool.Handler(ctx, arguments)
		}
	}
	return nil, errors.New("subagent built-in is unavailable")
}
