package toolexecution

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

func TestSkillLoaderIsAToolExecutionBuiltIn(t *testing.T) {
	loader := NewSkillLoader([]Skill{{
		Name: "testing-go", Description: "Use when testing Go.", Instructions: "Run the Go tests.",
	}}, inmemory.NewMessageStorage(), DefaultSkillReloadPolicy())
	tool := loader.Tool()
	if tool.Definition.Name != SkillLoaderToolName || tool.Handler == nil || !json.Valid(marshaledToolSchema(t, tool.Definition.InputSchema)) {
		t.Fatalf("invalid skill built-in: %#v", tool.Definition)
	}
	for _, expected := range []string{
		"exactly one skill",
		"available_skills",
		"applicable instruction requires",
		"description directly matches",
		"user asks to read it",
		"once per <turn_start>",
		"status=loaded",
		"instructions_in_context=true",
		"already in the conversation",
		"does not load any other skill",
	} {
		if !strings.Contains(tool.Definition.Description, expected) {
			t.Fatalf("load_skill description does not contain %q: %q", expected, tool.Definition.Description)
		}
	}
	if strings.Contains(tool.Definition.Description, "Continue the task") {
		t.Fatalf("load_skill description must not force post-load behavior: %q", tool.Definition.Description)
	}
	schema := string(marshaledToolSchema(t, tool.Definition.InputSchema))
	if !strings.Contains(schema, `"minLength":1`) ||
		!strings.Contains(schema, "Exact skill name from available_skills") ||
		!strings.Contains(schema, "after the latest") ||
		!strings.Contains(schema, "turn_start") {
		t.Fatalf("load_skill schema does not align with its triggers: %s", schema)
	}
	ctx := WithInvocation(context.Background(), Invocation{
		SessionID: "session", TurnID: "turn", CallID: "call", ToolName: SkillLoaderToolName,
	})
	output, err := tool.Handler(ctx, json.RawMessage(`{"name":"testing-go"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result SkillToolResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "loaded" || result.Name != "testing-go" ||
		result.Instructions != "Run the Go tests." ||
		result.Message != skillLoadedMessage("testing-go", false) {
		t.Fatalf("skill result = %s", output)
	}
	if result.InstructionsInContext {
		t.Fatalf("fresh skill result incorrectly points to prior context: %#v", result)
	}
	reusedOutput, err := tool.Handler(ctx, json.RawMessage(`{"name":"testing-go"}`))
	if err != nil {
		t.Fatal(err)
	}
	var reused SkillToolResult
	if err := json.Unmarshal(reusedOutput, &reused); err != nil {
		t.Fatal(err)
	}
	if reused.Status != "loaded" || reused.Instructions != "" || !reused.InstructionsInContext ||
		reused.Name != "testing-go" ||
		reused.Message != skillLoadedMessage("testing-go", true) {
		t.Fatalf("reused skill result does not identify instructions in context: %#v", reused)
	}
}

func TestTaskToolBridgeOwnsTheOnlyModelFacingSubagentTool(t *testing.T) {
	bridge := NewTaskToolBridge([]TaskAgent{
		{Name: "reviewer", Description: "Review changes."},
		{Name: "researcher", Description: "Find evidence."},
	})
	tools := bridge.Tools()
	if len(tools) != 1 {
		t.Fatalf("task tool count = %d, want 1", len(tools))
	}
	tool := tools[0]
	if tool.Definition.Name != TaskToolName || !IsSubagentToolName(TaskToolName) || tool.Handler == nil || !json.Valid(marshaledToolSchema(t, tool.Definition.InputSchema)) {
		t.Fatalf("invalid task built-in: %#v", tool.Definition)
	}
	for _, retired := range []string{"start_subagent", "send_subagent_message", "list_subagents", "subagent_status", "wait_subagent"} {
		if strings.Contains(tool.Definition.Name, retired) || IsSubagentToolName(retired) {
			t.Fatalf("retired model-facing tool %q remains exposed", retired)
		}
	}
	for _, expected := range []string{
		"new task", "task_id", "Foreground is the default", "same tool batch", "researcher: Find evidence.", "reviewer: Review changes.",
	} {
		if !strings.Contains(tool.Definition.Description, expected) {
			t.Fatalf("task description does not contain %q: %q", expected, tool.Definition.Description)
		}
	}
	if tool.Trigger != "" || tool.EndTurnOnSuccess || tool.resultTurnBehavior != nil {
		t.Fatalf("task must be a normal foreground tool: %#v", tool)
	}
	schema := string(marshaledToolSchema(t, tool.Definition.InputSchema))
	for _, expected := range []string{`"required":["prompt"]`, `"agent"`, `"description"`, `"task_id"`, `"background"`, `"additionalProperties":false`} {
		if !strings.Contains(schema, expected) {
			t.Fatalf("task schema does not contain %q: %s", expected, schema)
		}
	}
}

func TestTaskToolBridgeValidatesFormsAndReturnsBoundResult(t *testing.T) {
	bridge := NewTaskToolBridge(nil)
	bridge.Bind(func(_ context.Context, invocation Invocation, input TaskToolInput) (json.RawMessage, error) {
		if invocation.SessionID != "main" || invocation.TurnID != "turn" || input.Agent == nil || *input.Agent != "researcher" {
			t.Fatalf("executor input = %#v / %#v", invocation, input)
		}
		return json.RawMessage(`{"task_id":"task_1","agent":"researcher","state":"completed","output":"done","error":""}`), nil
	})
	tool := bridge.Tools()[0]
	ctx := WithInvocation(context.Background(), Invocation{SessionID: "main", TurnID: "turn", CallID: "call", ToolName: TaskToolName})
	output, err := tool.Handler(ctx, json.RawMessage(`{"agent":"researcher","description":"Research","prompt":"find it"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 5 || result["task_id"] != "task_1" || result["agent"] != "researcher" || result["state"] != "completed" || result["output"] != "done" || result["error"] != "" {
		t.Fatalf("task output = %s", output)
	}

	for _, test := range []struct {
		name      string
		arguments string
		want      string
	}{
		{"missing prompt", `{"agent":"researcher","description":"Research"}`, "task prompt is required"},
		{"new missing agent", `{"description":"Research","prompt":"find it"}`, "task agent is required"},
		{"new missing description", `{"agent":"researcher","prompt":"find it"}`, "task description is required"},
		{"resume includes agent", `{"task_id":"task_1","agent":"researcher","prompt":"continue"}`, "cannot be supplied"},
		{"resume includes description", `{"task_id":"task_1","description":"again","prompt":"continue"}`, "cannot be supplied"},
		{"empty task id", `{"task_id":" ","prompt":"continue"}`, "task_id cannot be empty"},
		{"unknown field", `{"agent":"researcher","description":"Research","prompt":"find it","extra":true}`, "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := tool.Handler(ctx, json.RawMessage(test.arguments))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Handler(%s) error = %v, want %q", test.arguments, err, test.want)
			}
		})
	}
}

func TestTaskToolBridgeRequiresTaskInvocationAndBinding(t *testing.T) {
	tool := NewTaskToolBridge(nil).Tools()[0]
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"agent":"researcher","description":"Research","prompt":"find it"}`)); err == nil || !strings.Contains(err.Error(), "requires tool invocation context") {
		t.Fatalf("missing invocation error = %v", err)
	}
	ctx := WithInvocation(context.Background(), Invocation{SessionID: "main", TurnID: "turn", CallID: "call", ToolName: TaskToolName})
	if _, err := tool.Handler(ctx, json.RawMessage(`{"agent":"researcher","description":"Research","prompt":"find it"}`)); err == nil || !strings.Contains(err.Error(), "task manager is unavailable") {
		t.Fatalf("unbound bridge error = %v", err)
	}
}

func TestTaskToolHandlersRunInParallelWithinExecutorWorkerCeiling(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	var running atomic.Int32
	var maximum atomic.Int32
	bridge := NewTaskToolBridge(nil)
	bridge.Bind(func(_ context.Context, invocation Invocation, _ TaskToolInput) (json.RawMessage, error) {
		current := running.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		entered <- invocation.CallID
		<-release
		running.Add(-1)
		return json.RawMessage(`{"task_id":"task","agent":"researcher","state":"completed","output":"done","error":""}`), nil
	})
	registry := NewRegistry()
	if err := registry.Register(bridge.Tools()[0]); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 2)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan agentruntime.ToolRequest, 2)
	results := make(chan agentruntime.ToolResultEnvelope, 2)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runExecutor(executor, ctx, requests, results, interrupts)
	for _, callID := range []string{"first", "second"} {
		requests <- toolRequest("main", "turn", callID, TaskToolName, `{"agent":"researcher","description":"Research","prompt":"work"}`)
	}
	started := map[string]bool{waitString(t, entered): true, waitString(t, entered): true}
	if !started["first"] || !started["second"] || maximum.Load() != 2 || maximum.Load() > 2 {
		t.Fatalf("task handlers did not run concurrently within the two-worker ceiling: started=%v max=%d", started, maximum.Load())
	}
	close(release)
	for range 2 {
		result := waitResult(t, results)
		if result.Result.Status != agentruntime.ToolResultSucceeded || result.Result.Name != TaskToolName {
			t.Fatalf("task result = %#v", result)
		}
	}
	cancel()
	waitDone(t, done)
}
