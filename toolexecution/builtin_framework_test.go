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
	if tool.Definition.Name != SkillLoaderToolName || tool.Handler == nil || !json.Valid(tool.Definition.InputSchema) {
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
	if len(tools) != 5 {
		t.Fatalf("subagent tool count = %d, want 5", len(tools))
	}
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if !IsSubagentToolName(tool.Definition.Name) || tool.Handler == nil || !json.Valid(tool.Definition.InputSchema) {
			t.Fatalf("invalid subagent built-in %q", tool.Definition.Name)
		}
		if tool.Definition.Name == StartSubagentToolName && !strings.Contains(tool.Definition.Description, "Do not use this tool for simple answers") {
			t.Fatalf("start_subagent does not discourage unnecessary delegation: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "always asynchronous") || strings.Contains(string(tool.Definition.InputSchema), `"background"`)) {
			t.Fatalf("start_subagent does not advertise its asynchronous default: %#v", tool.Definition)
		}
		if (tool.Definition.Name == StartSubagentToolName || tool.Definition.Name == SendSubagentMessageToolName) && tool.TurnBehavior != EndTurn {
			t.Fatalf("subagent dispatch tool %q turn behavior = %q, want end_turn", tool.Definition.Name, tool.TurnBehavior)
		}
		if tool.Definition.Name != StartSubagentToolName && tool.Definition.Name != SendSubagentMessageToolName && tool.TurnBehavior != ContinueTurn {
			t.Fatalf("subagent management tool %q turn behavior = %q, want continue", tool.Definition.Name, tool.TurnBehavior)
		}
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "exactly one open child is reused") || !strings.Contains(tool.Definition.Description, "selection_required") || !strings.Contains(string(tool.Definition.InputSchema), `"new_instance"`)) {
			t.Fatalf("start_subagent does not advertise reuse routing: %#v", tool.Definition)
		}
		if tool.Definition.Name == ListSubagentsToolName && (!strings.Contains(tool.Definition.Description, "explicit discovery") || !strings.Contains(tool.Definition.Description, "never use it as a polling loop")) {
			t.Fatalf("list_subagents does not prohibit polling: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == SubagentStatusToolName && (!strings.Contains(tool.Definition.Description, "explicitly asks") || !strings.Contains(tool.Definition.Description, "one fresh snapshot") || !strings.Contains(tool.Definition.Description, "already_checked")) {
			t.Fatalf("subagent_status does not advertise lightweight status semantics: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == SendSubagentMessageToolName && (!strings.Contains(tool.Definition.Description, "focused follow-up") || !strings.Contains(tool.Definition.Description, "not completed") || !strings.Contains(tool.Definition.Description, "do not call status/list/close")) {
			t.Fatalf("send_subagent_message does not describe callback-driven follow-up: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == CloseSubagentToolName && (!strings.Contains(tool.Definition.Description, "cleanup, not cancellation") || !strings.Contains(tool.Definition.Description, "Never call this after") || !strings.Contains(tool.Definition.Description, "completed callback") || !strings.Contains(tool.Definition.Description, "runtime rejects running")) {
			t.Fatalf("close_subagent does not describe lifecycle judgment: %q", tool.Definition.Description)
		}
		if strings.Contains(string(tool.Definition.InputSchema), `"type":"string"`) && !strings.Contains(string(tool.Definition.InputSchema), `"minLength":1`) {
			t.Fatalf("subagent tool %q has an unconstrained string schema: %s", tool.Definition.Name, tool.Definition.InputSchema)
		}
		seen[tool.Definition.Name] = true
	}
	for name := range subagentToolNames {
		if !seen[name] {
			t.Fatalf("reserved subagent tool %q is missing", name)
		}
	}
}

func TestStartSubagentOnlyContinuesWhenSelectionIsRequired(t *testing.T) {
	if got := startSubagentTurnBehavior(json.RawMessage(`{"action":"selection_required"}`)); got != ContinueTurn {
		t.Fatalf("selection behavior = %q, want continue", got)
	}
	for _, output := range []json.RawMessage{
		json.RawMessage(`{"action":"created"}`),
		json.RawMessage(`{"action":"reused"}`),
		json.RawMessage(`not-json`),
	} {
		if got := startSubagentTurnBehavior(output); got != EndTurn {
			t.Fatalf("dispatch behavior for %s = %q, want end_turn", output, got)
		}
	}
}
