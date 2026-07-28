package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/toolexecution"
)

func TestTaskToolBridgeValidatesInvocationAndExecutesForegroundTask(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 1)}
	manager := newTestSubagentManager(t, model, 2)
	defer manager.Close()
	bridge := newTestTaskToolBridge(manager)

	if _, err := callTaskTool(bridge, context.Background(), json.RawMessage(`{"agent":"researcher","description":"Research","prompt":"work"}`)); err == nil || !strings.Contains(err.Error(), "requires tool invocation context") {
		t.Fatalf("task without invocation error = %v", err)
	}
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "mainAgent-a", TurnID: "turn", CallID: "call", ToolName: TaskToolName,
	})
	results := make(chan json.RawMessage, 1)
	errs := make(chan error, 1)
	go func() {
		output, err := callTaskTool(bridge, ctx, json.RawMessage(`{"agent":"researcher","description":"Research","prompt":"work"}`))
		if err != nil {
			errs <- err
			return
		}
		results <- output
	}()
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	select {
	case output := <-results:
		t.Fatalf("foreground task returned before child completion: %s", output)
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(25 * time.Millisecond):
	}
	model.releases <- struct{}{}
	select {
	case err := <-errs:
		t.Fatal(err)
	case output := <-results:
		var result TaskResult
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		if result.TaskID == "" || result.AgentName != "researcher" || result.State != TaskStateCompleted || result.Output != "done" || result.Error != "" {
			t.Fatalf("task result = %s", output)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground task did not complete")
	}
}

func TestTaskToolBridgeRejectsInvalidNewAndResumeForms(t *testing.T) {
	manager := newTestSubagentManager(t, &scriptedModel{}, 1)
	defer manager.Close()
	bridge := newTestTaskToolBridge(manager)
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "main", TurnID: "turn", CallID: "call", ToolName: TaskToolName,
	})
	for _, test := range []struct {
		name      string
		arguments json.RawMessage
		want      string
	}{
		{"missing prompt", json.RawMessage(`{"agent":"researcher","description":"Research"}`), "task prompt is required"},
		{"new missing agent", json.RawMessage(`{"description":"Research","prompt":"work"}`), "task agent is required"},
		{"new missing description", json.RawMessage(`{"agent":"researcher","prompt":"work"}`), "task description is required"},
		{"resume includes agent", json.RawMessage(`{"task_id":"task_1","agent":"researcher","prompt":"continue"}`), "cannot be supplied"},
		{"resume includes description", json.RawMessage(`{"task_id":"task_1","description":"Research","prompt":"continue"}`), "cannot be supplied"},
		{"unknown field", json.RawMessage(`{"agent":"researcher","description":"Research","prompt":"work","continue_main_agent":false}`), "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := callTaskTool(bridge, ctx, test.arguments); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("task error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTaskToolIsTheOnlyReservedMainModelSubagentTool(t *testing.T) {
	if !isSubagentToolName(TaskToolName) {
		t.Fatal("task is not reserved")
	}
	for _, name := range []string{"start_subagent", "send_subagent_message", "list_subagents", "subagent_status", "wait_subagent"} {
		if isSubagentToolName(name) {
			t.Fatalf("retired model tool %q remains reserved", name)
		}
	}
	tools := toolexecution.NewTaskToolBridge([]toolexecution.TaskAgent{{Name: "researcher", Description: "Find facts."}}).Tools()
	if len(tools) != 1 || tools[0].Definition.Name != TaskToolName {
		t.Fatalf("main model tools = %#v, want only task", tools)
	}
}

func newTestTaskToolBridge(manager *subagentManager) *toolexecution.TaskToolBridge {
	bridge := toolexecution.NewTaskToolBridge([]toolexecution.TaskAgent{{Name: "researcher", Description: "Research facts."}})
	bridge.Bind(func(ctx context.Context, invocation toolexecution.Invocation, input toolexecution.TaskToolInput) (json.RawMessage, error) {
		request := TaskRequest{MainAgentSessionID: invocation.SessionID, MainAgentTurnID: invocation.TurnID, Prompt: input.Prompt, Background: input.Background}
		if input.Agent != nil {
			request.AgentName = *input.Agent
		}
		if input.Description != nil {
			request.Description = *input.Description
		}
		if input.TaskID != nil {
			request.TaskID = *input.TaskID
		}
		result, err := manager.ExecuteTask(ctx, request)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	})
	return bridge
}

func callTaskTool(bridge *toolexecution.TaskToolBridge, ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	for _, tool := range bridge.Tools() {
		if tool.Definition.Name == TaskToolName {
			return tool.Handler(ctx, arguments)
		}
	}
	return nil, errors.New("task built-in is unavailable")
}
