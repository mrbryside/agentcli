package toolexecution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
	if result.Status != "loaded" || result.Instructions != "Run the Go tests." {
		t.Fatalf("skill result = %s", output)
	}
}

func TestSubagentToolBridgeOwnsCompleteReservedCatalog(t *testing.T) {
	tools := NewSubagentToolBridge().Tools()
	if len(tools) != 4 {
		t.Fatalf("subagent tool count = %d, want 4", len(tools))
	}
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if !IsSubagentToolName(tool.Definition.Name) || tool.Handler == nil || !json.Valid(marshaledToolSchema(t, tool.Definition.InputSchema)) {
			t.Fatalf("invalid subagent built-in %q", tool.Definition.Name)
		}
		if tool.Definition.Name == StartSubagentToolName && !strings.Contains(tool.Definition.Description, "Do not use this tool for simple answers") {
			t.Fatalf("start_subagent does not discourage unnecessary delegation: %q", tool.Definition.Description)
		}
		schema := string(marshaledToolSchema(t, tool.Definition.InputSchema))
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "always asynchronous") || strings.Contains(schema, `"background"`)) {
			t.Fatalf("start_subagent does not advertise its asynchronous default: %#v", tool.Definition)
		}
		if tool.Definition.Name == StartSubagentToolName || tool.Definition.Name == SendSubagentMessageToolName {
			if tool.Trigger != "" || tool.EndTurnOnSuccess || tool.resultTurnBehavior != nil || strings.Contains(schema, `"finish_turn"`) || !strings.Contains(tool.Definition.Description, "always continues") {
				t.Fatalf("subagent operation %q must always continue without finish_turn: %#v", tool.Definition.Name, tool)
			}
		}
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "exactly one start_subagent call per provider round") || !strings.Contains(tool.Definition.Description, "Never emit multiple start_subagent calls in the same tool batch")) {
			t.Fatalf("start_subagent does not describe sequential starts: %#v", tool)
		}
		if tool.Definition.Name != StartSubagentToolName && tool.Definition.Name != SendSubagentMessageToolName && (tool.Trigger != "" || tool.EndTurnOnSuccess) {
			t.Fatalf("subagent management tool %q has terminal behavior, want default continue", tool.Definition.Name)
		}
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "exactly one open child of the requested definition is reused") || !strings.Contains(tool.Definition.Description, "selection_required") || !strings.Contains(tool.Definition.Description, "accepted=true") || !strings.Contains(schema, `"new_instance"`)) {
			t.Fatalf("start_subagent does not advertise reuse routing: %#v", tool.Definition)
		}
		if tool.Definition.Name == ListSubagentsToolName && (!strings.Contains(tool.Definition.Description, "explicit discovery") || !strings.Contains(tool.Definition.Description, "never use it as a polling loop")) {
			t.Fatalf("list_subagents does not prohibit polling: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == SubagentStatusToolName && (!strings.Contains(tool.Definition.Description, "explicitly asks") || !strings.Contains(tool.Definition.Description, "one fresh snapshot") || !strings.Contains(tool.Definition.Description, "already_checked")) {
			t.Fatalf("subagent_status does not advertise lightweight status semantics: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == SendSubagentMessageToolName && (!strings.Contains(tool.Definition.Description, "focused follow-up") || !strings.Contains(tool.Definition.Description, "not completed") || !strings.Contains(tool.Definition.Description, "Do not redo delegated work") || !strings.Contains(tool.Definition.Description, "action=callback_pending") || !strings.Contains(tool.Definition.Description, "successful controlled result") || !strings.Contains(tool.Definition.Description, "already-planned independent work")) {
			t.Fatalf("send_subagent_message does not describe callback-driven follow-up: %q", tool.Definition.Description)
		}
		if (tool.Definition.Name == StartSubagentToolName || tool.Definition.Name == SendSubagentMessageToolName) && (!strings.Contains(tool.Definition.Description, "neither duplicates the delegated task nor depends on its result") || !strings.Contains(tool.Definition.Description, "A callback cannot arrive until the current parent turn ends")) {
			t.Fatalf("asynchronous dispatch tool %q does not constrain work while waiting: %q", tool.Definition.Name, tool.Definition.Description)
		}
		schema = string(marshaledToolSchema(t, tool.Definition.InputSchema))
		if strings.Contains(schema, `"type":"string"`) && !strings.Contains(schema, `"minLength":1`) {
			t.Fatalf("subagent tool %q has an unconstrained string schema: %s", tool.Definition.Name, schema)
		}
		seen[tool.Definition.Name] = true
	}
	for name := range subagentToolNames {
		if !seen[name] {
			t.Fatalf("reserved subagent tool %q is missing", name)
		}
	}
}

func TestSubagentToolTurnBehavior(t *testing.T) {
	for _, tool := range NewSubagentToolBridge().Tools() {
		if tool.Trigger != "" || tool.EndTurnOnSuccess || tool.resultTurnBehavior != nil {
			t.Fatalf("%s terminal behavior = (trigger=%q, end_on_success=%t, dynamic=%v), want default continue", tool.Definition.Name, tool.Trigger, tool.EndTurnOnSuccess, tool.resultTurnBehavior != nil)
		}
	}
}
