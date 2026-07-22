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
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "exactly one open child is reused") || !strings.Contains(tool.Definition.Description, "selection_required") || !strings.Contains(string(tool.Definition.InputSchema), `"new_instance"`)) {
			t.Fatalf("start_subagent does not advertise reuse routing: %#v", tool.Definition)
		}
		if tool.Definition.Name == ListSubagentsToolName && (!strings.Contains(tool.Definition.Description, "explicitly asks") || !strings.Contains(tool.Definition.Description, "Never call it to wait, poll")) {
			t.Fatalf("list_subagents does not prohibit polling: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == SubagentStatusToolName && (!strings.Contains(tool.Definition.Description, "explicitly asks") || !strings.Contains(tool.Definition.Description, "at most once") || !strings.Contains(tool.Definition.Description, "never poll")) {
			t.Fatalf("subagent_status does not advertise lightweight status semantics: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == SendSubagentMessageToolName && (!strings.Contains(tool.Definition.Description, "focused follow-up") || !strings.Contains(tool.Definition.Description, "Do not poll")) {
			t.Fatalf("send_subagent_message does not describe callback-driven follow-up: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == CloseSubagentToolName && (!strings.Contains(tool.Definition.Description, "bounded one-shot work") || !strings.Contains(tool.Definition.Description, "mere possibility")) {
			t.Fatalf("close_subagent does not describe lifecycle judgment: %q", tool.Definition.Description)
		}
		seen[tool.Definition.Name] = true
	}
	for name := range subagentToolNames {
		if !seen[name] {
			t.Fatalf("reserved subagent tool %q is missing", name)
		}
	}
}
