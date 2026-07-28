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

	if _, err := callSubagentTool(bridge, StartSubagentToolName, context.Background(), json.RawMessage(`{"name":"researcher","message":"work","continue_main_agent":true}`)); err == nil {
		t.Fatal("start without invocation context error = nil")
	}
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "mainAgent-a", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
	output, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"work","continue_main_agent":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID              string                 `json:"subagent_id"`
		Status          storage.SubagentStatus `json:"status"`
		ResultDelivery  string                 `json:"result_delivery"`
		MainAgentAction string                 `json:"main_agent_action"`
		Instruction     string                 `json:"instruction"`
	}
	if err := json.Unmarshal(output, &started); err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.Status != storage.SubagentStatusRunning || started.ResultDelivery != "automatic" || started.MainAgentAction != "continue_independent_work" || !isContinueSubagentInstruction(started.Instruction) || strings.Contains(string(output), `"finish_turn"`) || strings.Contains(string(output), `"must_wait_for_result"`) || strings.Contains(string(output), `"turn_action"`) {
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

func TestStartSubagentToolAlwaysCreatesNewSubagent(t *testing.T) {
	t.Run("one subagent", func(t *testing.T) {
		manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 4)
		defer manager.Close()
		bridge := newTestSubagentToolBridge(manager)
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "mainAgent", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
		firstJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"first","continue_main_agent":true}`))
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
		duplicateJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":" first ","continue_main_agent":true}`))
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
		secondJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"talk more","continue_main_agent":true}`))
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
		nextTurnCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "mainAgent", TurnID: "turn-2", CallID: "call-2", ToolName: StartSubagentToolName})
		acceptedJSON, err := callSubagentTool(bridge, StartSubagentToolName, nextTurnCtx, json.RawMessage(`{"name":"researcher","message":"talk more","continue_main_agent":true}`))
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
			for _, obsolete := range []string{`"reused"`, `"deduplicated"`, `"assignment_action"`, `"candidates"`} {
				if strings.Contains(string(output), obsolete) {
					t.Fatalf("start result contains obsolete field %s: %s", obsolete, output)
				}
			}
		}
	})

	t.Run("many subagents", func(t *testing.T) {
		manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 3)
		defer manager.Close()
		bridge := newTestSubagentToolBridge(manager)
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "mainAgent", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
		for _, message := range []string{"first", "second"} {
			arguments := json.RawMessage(`{"name":"researcher","message":"` + message + `","continue_main_agent":true}`)
			if _, err := callSubagentTool(bridge, StartSubagentToolName, ctx, arguments); err != nil {
				t.Fatal(err)
			}
		}
		selectionJSON, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"talk more","continue_main_agent":true}`))
		if err != nil {
			t.Fatal(err)
		}
		var selection struct {
			Action          toolexecution.SubagentStartAction `json:"action"`
			MainAgentAction string                            `json:"main_agent_action"`
			Instruction     string                            `json:"instruction"`
			Accepted        bool                              `json:"accepted"`
			ResultDelivery  string                            `json:"result_delivery"`
		}
		if err := json.Unmarshal(selectionJSON, &selection); err != nil {
			t.Fatal(err)
		}
		if selection.Action != toolexecution.SubagentStartCreated || !selection.Accepted || selection.ResultDelivery != "automatic" || selection.MainAgentAction != "continue_independent_work" || strings.Contains(string(selectionJSON), `"finish_turn"`) || !strings.Contains(selection.Instruction, "specific independent main-agent work already planned") {
			t.Fatalf("selection = %s", selectionJSON)
		}
		for _, forbidden := range []string{`"last_turn_error"`, `"last_turn_outcome"`, `"last_turn_summary"`, `"last_turn_next_step"`} {
			if strings.Contains(string(selectionJSON), forbidden) {
				t.Fatalf("selection leaked result payload field %s: %s", forbidden, selectionJSON)
			}
		}
	})

	t.Run("legacy new_instance is rejected", func(t *testing.T) {
		manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 1)
		defer manager.Close()
		bridge := newTestSubagentToolBridge(manager)
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "mainAgent", TurnID: "turn", CallID: "call", ToolName: StartSubagentToolName})
		if _, err := callSubagentTool(bridge, StartSubagentToolName, ctx, json.RawMessage(`{"name":"researcher","message":"work","new_instance":true,"continue_main_agent":true}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("legacy new_instance error = %v", err)
		}
	})
}

func TestSendSubagentMessageToolOnlyTargetsIdleSubagentAfterResult(t *testing.T) {
	manager := newTestSubagentManager(t, &subagentGateModel{releases: make(chan struct{})}, 2)
	defer manager.Close()
	bridge := newTestSubagentToolBridge(manager)
	startCtx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "mainAgent", TurnID: "turn-1", CallID: "start", ToolName: StartSubagentToolName})
	startedJSON, err := callSubagentTool(bridge, StartSubagentToolName, startCtx, json.RawMessage(`{"name":"researcher","message":"work","continue_main_agent":true}`))
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
		Instruction     string `json:"instruction"`
		ResultDelivery  string `json:"result_delivery"`
		MainAgentAction string `json:"main_agent_action"`
	}
	send := func(turnID, callID, message string) sendResult {
		t.Helper()
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{SessionID: "mainAgent", TurnID: turnID, CallID: callID, ToolName: SendSubagentMessageToolName})
		arguments, err := json.Marshal(map[string]any{"subagent_id": started.ID, "message": message, "continue_main_agent": true})
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
	if duplicate := send("turn-1", "duplicate", " work "); duplicate.Action != toolexecution.SubagentSendDuplicate || duplicate.Accepted || !duplicate.Deduplicated || duplicate.Subagent.QueuedMessages != 0 || duplicate.MainAgentAction != "stop_and_wait" {
		t.Fatalf("duplicate = %#v", duplicate)
	}
	if changed := send("turn-1", "changed", "wait for the result"); changed.Action != toolexecution.SubagentSendAlreadySent || changed.Accepted || changed.Deduplicated || changed.Subagent.QueuedMessages != 0 || changed.MainAgentAction != "stop_and_wait" || !isStopAndWaitInstruction(changed.Instruction) {
		t.Fatalf("changed repeat = %#v", changed)
	}
	if pending := send("turn-2", "pending", "next task"); pending.Action != toolexecution.SubagentSendResultPending || pending.Accepted || pending.Deduplicated || pending.Subagent.QueuedMessages != 0 || pending.ResultDelivery != "existing" || pending.MainAgentAction != "stop_and_wait" || !isStopAndWaitInstruction(pending.Instruction) {
		t.Fatalf("next turn = %#v", pending)
	}
}

func TestSendSubagentMessageToolReturnsResultPendingAsControlledResult(t *testing.T) {
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
	bridge := newTestSubagentToolBridge(manager)

	test := func(turnID string) {
		t.Helper()
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
			SessionID: "mainAgent", TurnID: turnID, CallID: "send", ToolName: SendSubagentMessageToolName,
		})
		arguments, err := json.Marshal(map[string]any{"subagent_id": record.ID, "message": "too early", "continue_main_agent": true})
		if err != nil {
			t.Fatal(err)
		}
		output, err := callSubagentTool(bridge, SendSubagentMessageToolName, ctx, arguments)
		if err != nil {
			t.Fatalf("result_pending returned tool error: %v", err)
		}
		var result struct {
			Action          toolexecution.SubagentSendAction `json:"action"`
			Accepted        bool                             `json:"accepted"`
			ResultDelivery  string                           `json:"result_delivery"`
			MainAgentAction string                           `json:"main_agent_action"`
			Instruction     string                           `json:"instruction"`
		}
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		if result.Action != toolexecution.SubagentSendResultPending || result.Accepted || result.ResultDelivery != "existing" || result.MainAgentAction != "stop_and_wait" || strings.Contains(string(output), `"finish_turn"`) || !isStopAndWaitInstruction(result.Instruction) {
			t.Fatalf("result_pending result = %s", output)
		}
		for _, forbidden := range []string{`"last_turn_error"`, `"last_turn_outcome"`, `"last_turn_summary"`, `"last_turn_next_step"`} {
			if strings.Contains(string(output), forbidden) {
				t.Fatalf("result_pending leaked result payload field %s: %s", forbidden, output)
			}
		}
	}

	test("result-pending-turn")
}

func TestSendSubagentMessageToolRejectsCompletedSubagentReuse(t *testing.T) {
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

	bridge := newTestSubagentToolBridge(manager)
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "mainAgent", TurnID: "follow-up-turn", CallID: "send", ToolName: SendSubagentMessageToolName,
	})
	arguments, err := json.Marshal(map[string]any{"subagent_id": record.ID, "message": "unrelated next task", "continue_main_agent": true})
	if err != nil {
		t.Fatal(err)
	}
	output, err := callSubagentTool(bridge, SendSubagentMessageToolName, ctx, arguments)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Action          toolexecution.SubagentSendAction `json:"action"`
		Accepted        bool                             `json:"accepted"`
		ResultDelivery  string                           `json:"result_delivery"`
		MainAgentAction string                           `json:"main_agent_action"`
		Instruction     string                           `json:"instruction"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != toolexecution.SubagentSendCompleted || result.Accepted || result.ResultDelivery != "none" ||
		result.MainAgentAction != "deliver_completed_result" ||
		!strings.Contains(result.Instruction, "must not be reused") || !strings.Contains(result.Instruction, "Deliver its completed result") {
		t.Fatalf("completed-subagent result = %s", output)
	}
}

func TestReadSubagentRecoveryAPIReportsLastTurnFailure(t *testing.T) {
	manager := newTestSubagentManager(t, subagentFailModel{err: errors.New("provider unavailable")}, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "inspect project", "")
	if err != nil {
		t.Fatal(err)
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	result, err := manager.Read(context.Background(), "mainAgent", record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Subagent.LastSubagentTurnID == "" || !strings.Contains(result.Subagent.LastResultError, "provider unavailable") {
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
		{"start background", StartSubagentToolName, json.RawMessage(`{"name":"researcher","message":"summarize README","continue_main_agent":true,"background":false}`)},
		{"start finish", StartSubagentToolName, json.RawMessage(`{"name":"researcher","message":"summarize README","continue_main_agent":true,"finish_turn":true}`)},
		{"send finish", SendSubagentMessageToolName, json.RawMessage(`{"subagent_id":"subagent","message":"continue","continue_main_agent":true,"finish_turn":true}`)},
	}
	for _, test := range tests {
		ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
			SessionID: "mainAgent", TurnID: "mainAgent-turn", CallID: test.name, ToolName: test.toolName,
		})
		if _, err := callSubagentTool(bridge, test.toolName, ctx, test.arguments); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("%s removed option error = %v", test.name, err)
		}
	}
}

func TestStartSubagentRequiresExplicitContinueAfterAssignmentChoice(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "mainAgent", TurnID: "mainAgent-turn", CallID: "start", ToolName: StartSubagentToolName,
	})
	_, err := callSubagentTool(
		bridge,
		StartSubagentToolName,
		ctx,
		json.RawMessage(`{"name":"researcher","message":"work"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "continue_main_agent is required") {
		t.Fatalf("missing continue_main_agent error = %v", err)
	}
}

func TestSendSubagentMessageRequiresExplicitContinueAfterAssignmentChoice(t *testing.T) {
	bridge := toolexecution.NewSubagentToolBridge()
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "mainAgent", TurnID: "mainAgent-turn", CallID: "send", ToolName: SendSubagentMessageToolName,
	})
	_, err := callSubagentTool(
		bridge,
		SendSubagentMessageToolName,
		ctx,
		json.RawMessage(`{"subagent_id":"subagent","message":"continue"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "continue_main_agent is required") {
		t.Fatalf("missing continue_main_agent error = %v", err)
	}
}

func newTestSubagentToolBridge(manager *subagentManager) *toolexecution.SubagentToolBridge {
	bridge := toolexecution.NewSubagentToolBridge()
	bridge.Bind(manager)
	return bridge
}

func appendMainAgentUserMessage(t *testing.T, manager *subagentManager, sessionID, turnID, content string) {
	t.Helper()
	err := manager.mainAgent.messages.Append(context.Background(), agentruntime.Message{
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

func appendMainAgentRuntimeMessage(t *testing.T, manager *subagentManager, sessionID, turnID, content string) {
	t.Helper()
	err := manager.mainAgent.messages.Append(context.Background(), agentruntime.Message{
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

func isStopAndWaitInstruction(instruction string) bool {
	for _, expected := range []string{
		"Not accepted. No new work started.",
		"Stop now",
		"existing subagent result will arrive automatically",
		"Do not retry, call another tool, or generate assistant content",
	} {
		if !strings.Contains(instruction, expected) {
			return false
		}
	}
	return true
}

func isContinueSubagentInstruction(instruction string) bool {
	for _, expected := range []string{
		"subagent result will arrive automatically",
		"specific independent main-agent work already planned",
		"When it is finished, stop",
		"merely to simulate waiting",
	} {
		if !strings.Contains(instruction, expected) {
			return false
		}
	}
	return true
}
