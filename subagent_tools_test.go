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

	if _, err := callSubagentTool(bridge, StartSubagentToolName, context.Background(), json.RawMessage(`{"name":"researcher","message":"work","continue_after_dispatch":true}`)); err == nil {
		t.Fatal("start without invocation context error = nil")
	}
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent-a", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
	output, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"work","continue_after_dispatch":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID           string                 `json:"subagent_id"`
		Status       storage.SubagentStatus `json:"status"`
		Asynchronous bool                   `json:"asynchronous"`
		Callback     string                 `json:"callback_action"`
		MustWait     bool                   `json:"must_wait_for_callback"`
		TurnAction   string                 `json:"turn_action"`
		NextAction   string                 `json:"next_action"`
	}
	if err := json.Unmarshal(output, &started); err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.Status != storage.SubagentStatusRunning || !started.Asynchronous || started.Callback != "automatic" || !started.MustWait || started.TurnAction != "continue_independent_work" || !isContinueCallbackInstruction(started.NextAction) || strings.Contains(string(output), `"finish_turn"`) || strings.Contains(string(output), `"prohibited_actions"`) || strings.Contains(string(output), `"turn_behavior"`) {
		t.Fatalf("start result = %s", output)
	}
	if strings.Contains(string(output), "close_subagent") {
		t.Fatalf("removed destructive tool appears in start acknowledgement: %s", output)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, started.ID, storage.SubagentStatusIdle)
}

func TestSubagentToolFactoriesAreCompleteAndReserved(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	tools := bridge.Tools()
	if len(tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(tools))
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
	if seen[ListSubagentsToolName] || seen[SubagentStatusToolName] {
		t.Fatalf("inspection tools remain model-facing: %#v", seen)
	}
}

func TestStartSubagentToolAlwaysCreatesNewChild(t *testing.T) {
	t.Run("one child", func(t *testing.T) {
		manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 4)
		defer manager.Close()
		bridge := newTestSubagentToolBridge(manager)
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
		firstJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"first","continue_after_dispatch":true}`))
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
		duplicateJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":" first ","continue_after_dispatch":true}`))
		if err != nil {
			t.Fatal(err)
		}
		var duplicate struct {
			ID       string                            `json:"subagent_id"`
			Action   toolexecution.SubagentStartAction `json:"action"`
			Accepted bool                              `json:"accepted"`
		}
		if err := json.Unmarshal(duplicateJSON, &duplicate); err != nil {
			t.Fatal(err)
		}
		secondJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"talk more","continue_after_dispatch":true}`))
		if err != nil {
			t.Fatal(err)
		}
		var second struct {
			ID       string                            `json:"subagent_id"`
			Action   toolexecution.SubagentStartAction `json:"action"`
			Accepted bool                              `json:"accepted"`
		}
		if err := json.Unmarshal(secondJSON, &second); err != nil {
			t.Fatal(err)
		}
		nextTurnCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "turn-2", CallID: "call-2", ToolName: StartSubagentToolName})
		acceptedJSON, err := callSubagentTool(bridge, StartSubagentToolName, nextTurnCtx, json.RawMessage(`{"name":"researcher","message":"talk more","continue_after_dispatch":true}`))
		if err != nil {
			t.Fatal(err)
		}
		var accepted struct {
			ID       string                            `json:"subagent_id"`
			Action   toolexecution.SubagentStartAction `json:"action"`
			Accepted bool                              `json:"accepted"`
		}
		if err := json.Unmarshal(acceptedJSON, &accepted); err != nil {
			t.Fatal(err)
		}
		if first.ID == "" || first.DisplayName == "" || first.Action != toolexecution.SubagentStartCreated || !first.Accepted ||
			duplicate.ID == "" || duplicate.ID == first.ID || duplicate.Action != toolexecution.SubagentStartCreated || !duplicate.Accepted ||
			second.ID == "" || second.ID == first.ID || second.ID == duplicate.ID || second.Action != toolexecution.SubagentStartCreated || !second.Accepted ||
			accepted.ID == "" || accepted.ID == first.ID || accepted.ID == duplicate.ID || accepted.ID == second.ID || accepted.Action != toolexecution.SubagentStartCreated || !accepted.Accepted {
			t.Fatalf("first = %s, duplicate = %s, second = %s, accepted = %s", firstJSON, duplicateJSON, secondJSON, acceptedJSON)
		}
		for _, output := range [][]byte{firstJSON, duplicateJSON, secondJSON, acceptedJSON} {
			for _, obsolete := range []string{`"reused"`, `"deduplicated"`, `"dispatch_action"`, `"candidates"`} {
				if strings.Contains(string(output), obsolete) {
					t.Fatalf("start result contains obsolete field %s: %s", obsolete, output)
				}
			}
		}
	})

	t.Run("many children", func(t *testing.T) {
		manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 3)
		defer manager.Close()
		bridge := newTestSubagentToolBridge(manager)
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
		for _, message := range []string{"first", "second"} {
			arguments := json.RawMessage(`{"name":"researcher","message":"` + message + `","continue_after_dispatch":true}`)
			if _, err := callSubagentTool(bridge, StartSubagentToolName, ctx, arguments); err != nil {
				t.Fatal(err)
			}
		}
		selectionJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"talk more","continue_after_dispatch":true}`))
		if err != nil {
			t.Fatal(err)
		}
		var selection struct {
			Action       toolexecution.SubagentStartAction   `json:"action"`
			Candidates   []toolexecution.SubagentToolSummary `json:"candidates"`
			Behavior     string                              `json:"turn_behavior"`
			TurnAction   string                              `json:"turn_action"`
			NextAction   string                              `json:"next_action"`
			Accepted     bool                                `json:"accepted"`
			Deduplicated bool                                `json:"deduplicated"`
			Callback     string                              `json:"callback_action"`
			MustWait     bool                                `json:"must_wait_for_callback"`
		}
		if err := json.Unmarshal(selectionJSON, &selection); err != nil {
			t.Fatal(err)
		}
		if selection.Action != toolexecution.SubagentStartCreated || !selection.Accepted || selection.Deduplicated || selection.Callback != "automatic" || !selection.MustWait || selection.Behavior != "" || selection.TurnAction != "continue_independent_work" || strings.Contains(string(selectionJSON), `"finish_turn"`) || len(selection.Candidates) != 0 || !strings.Contains(selection.NextAction, "already-planned parent work") {
			t.Fatalf("selection = %s", selectionJSON)
		}
		for _, forbidden := range []string{`"last_turn_error"`, `"last_turn_outcome"`, `"last_turn_summary"`, `"last_turn_next_step"`} {
			if strings.Contains(string(selectionJSON), forbidden) {
				t.Fatalf("selection leaked callback payload field %s: %s", forbidden, selectionJSON)
			}
		}
	})

	t.Run("legacy new_instance is rejected", func(t *testing.T) {
		manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 1)
		defer manager.Close()
		bridge := newTestSubagentToolBridge(manager)
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
		if _, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"work","new_instance":true,"continue_after_dispatch":true}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("legacy new_instance error = %v", err)
		}
	})
}

func TestSendSubagentMessageToolOnlyTargetsIdleChildAfterCallback(t *testing.T) {
	manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 2)
	defer manager.Close()
	bridge := newTestSubagentToolBridge(manager)
	startCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: "turn-1", CallID: "start", ToolName: StartSubagentToolName})
	startedJSON, err := callSubagentTool(bridge, StartSubagentToolName, startCtx, json.RawMessage(`{"name":"researcher","message":"work","continue_after_dispatch":true}`))
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
		TurnAction  string `json:"turn_action"`
	}
	send := func(turnID, callID, message string) sendResult {
		t.Helper()
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "parent", TurnID: turnID, CallID: callID, ToolName: SendSubagentMessageToolName})
		arguments, err := json.Marshal(map[string]any{"subagent_id": started.ID, "message": message, "continue_after_dispatch": true})
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
	if duplicate := send("turn-1", "duplicate", " work "); duplicate.Action != toolexecution.SubagentSendDuplicate || duplicate.Accepted || !duplicate.Deduplicated || duplicate.Subagent.QueuedMessages != 0 || duplicate.TurnAction != "end_turn_wait_for_callback" {
		t.Fatalf("duplicate = %#v", duplicate)
	}
	if changed := send("turn-1", "changed", "wait for the result"); changed.Action != toolexecution.SubagentSendAlreadySent || changed.Accepted || changed.Deduplicated || changed.Subagent.QueuedMessages != 0 || changed.TurnAction != "end_turn_wait_for_callback" || !isEndedCallbackInstruction(changed.Instruction) {
		t.Fatalf("changed repeat = %#v", changed)
	}
	if pending := send("turn-2", "pending", "next task"); pending.Action != toolexecution.SubagentSendCallbackPending || pending.Accepted || pending.Deduplicated || pending.Subagent.QueuedMessages != 0 || pending.Callback != "automatic_existing" || !pending.MustWait || pending.TurnAction != "end_turn_wait_for_callback" || !isEndedCallbackInstruction(pending.Instruction) {
		t.Fatalf("next turn = %#v", pending)
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
		arguments, err := json.Marshal(map[string]any{"subagent_id": record.ID, "message": "too early", "continue_after_dispatch": true})
		if err != nil {
			t.Fatal(err)
		}
		output, err := callSubagentTool(bridge, SendSubagentMessageToolName, ctx, arguments)
		if err != nil {
			t.Fatalf("callback_pending returned tool error: %v", err)
		}
		var result struct {
			Action      toolexecution.SubagentSendAction `json:"action"`
			Accepted    bool                             `json:"accepted"`
			Callback    string                           `json:"callback_action"`
			MustWait    bool                             `json:"must_wait_for_callback"`
			TurnAction  string                           `json:"turn_action"`
			Instruction string                           `json:"instruction"`
		}
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		if result.Action != toolexecution.SubagentSendCallbackPending || result.Accepted || result.Callback != "automatic_existing" || !result.MustWait || result.TurnAction != "end_turn_wait_for_callback" || strings.Contains(string(output), `"finish_turn"`) || !isEndedCallbackInstruction(result.Instruction) {
			t.Fatalf("callback_pending result = %s", output)
		}
		for _, forbidden := range []string{`"last_turn_error"`, `"last_turn_outcome"`, `"last_turn_summary"`, `"last_turn_next_step"`} {
			if strings.Contains(string(output), forbidden) {
				t.Fatalf("callback_pending leaked callback payload field %s: %s", forbidden, output)
			}
		}
	}

	test("callback-pending-turn")
}

func TestSendSubagentMessageToolRejectsCompletedChildReuse(t *testing.T) {
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
	observeTestSubagentCallback(t, manager, markTestSubagentCompleted(t, manager, record.ID))

	bridge := newTestSubagentToolBridge(manager)
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "parent", TurnID: "follow-up-turn", CallID: "send", ToolName: SendSubagentMessageToolName,
	})
	arguments, err := json.Marshal(map[string]any{"subagent_id": record.ID, "message": "unrelated next task", "continue_after_dispatch": true})
	if err != nil {
		t.Fatal(err)
	}
	output, err := callSubagentTool(bridge, SendSubagentMessageToolName, ctx, arguments)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Action      toolexecution.SubagentSendAction `json:"action"`
		Accepted    bool                             `json:"accepted"`
		Callback    string                           `json:"callback_action"`
		MustWait    bool                             `json:"must_wait_for_callback"`
		TurnAction  string                           `json:"turn_action"`
		Instruction string                           `json:"instruction"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != toolexecution.SubagentSendChildCompleted || result.Accepted || result.Callback != "none" || result.MustWait ||
		result.TurnAction != "continue_to_deliver_completed_result" ||
		!strings.Contains(result.Instruction, "must not be reused") || !strings.Contains(result.Instruction, "Deliver its completed result") {
		t.Fatalf("completed-child result = %s", output)
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
		{"start background", StartSubagentToolName, json.RawMessage(`{"name":"researcher","message":"summarize README","continue_after_dispatch":true,"background":false}`)},
		{"start finish", StartSubagentToolName, json.RawMessage(`{"name":"researcher","message":"summarize README","continue_after_dispatch":true,"finish_turn":true}`)},
		{"send finish", SendSubagentMessageToolName, json.RawMessage(`{"subagent_id":"child","message":"continue","continue_after_dispatch":true,"finish_turn":true}`)},
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

func TestStartSubagentRequiresExplicitContinueAfterDispatchChoice(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "parent", TurnID: "parent-turn", CallID: "start", ToolName: StartSubagentToolName,
	})
	_, err := callSubagentTool(
		bridge,
		StartSubagentToolName,
		ctx,
		json.RawMessage(`{"name":"researcher","message":"work"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "continue_after_dispatch is required") {
		t.Fatalf("missing continue_after_dispatch error = %v", err)
	}
}

func TestSendSubagentMessageRequiresExplicitContinueAfterDispatchChoice(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "parent", TurnID: "parent-turn", CallID: "send", ToolName: SendSubagentMessageToolName,
	})
	_, err := callSubagentTool(
		bridge,
		SendSubagentMessageToolName,
		ctx,
		json.RawMessage(`{"subagent_id":"child","message":"continue"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "continue_after_dispatch is required") {
		t.Fatalf("missing continue_after_dispatch error = %v", err)
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

func isEndedCallbackInstruction(instruction string) bool {
	for _, expected := range []string{
		"No new work was dispatched",
		"existing result will arrive automatically later",
		"successful tool batch will end automatically",
		"Do not retry, call another tool, or generate assistant content",
	} {
		if !strings.Contains(instruction, expected) {
			return false
		}
	}
	return true
}

func isContinueCallbackInstruction(instruction string) bool {
	for _, expected := range []string{
		"result will arrive automatically later",
		"continue_after_dispatch=true",
		"already-planned parent work",
		"outside the delegated task",
		"Do not invent work, poll, or narrate waiting",
	} {
		if !strings.Contains(instruction, expected) {
			return false
		}
	}
	return true
}
