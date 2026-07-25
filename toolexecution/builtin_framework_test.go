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
	if len(tools) != 6 {
		t.Fatalf("subagent tool count = %d, want 6", len(tools))
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
		if tool.Definition.Name == StartSubagentToolName || tool.Definition.Name == SendSubagentMessageToolName || tool.Definition.Name == ForceCloseSubagentToolName {
			if tool.TurnBehavior != ContinueTurn || tool.resultTurnBehavior != nil || strings.Contains(schema, `"finish_turn"`) || !strings.Contains(tool.Definition.Description, "always continues") {
				t.Fatalf("subagent operation %q must always continue without finish_turn: %#v", tool.Definition.Name, tool)
			}
		}
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "exactly one start_subagent call per provider round") || !strings.Contains(tool.Definition.Description, "Never emit multiple start_subagent calls in the same tool batch")) {
			t.Fatalf("start_subagent does not describe sequential starts: %#v", tool)
		}
		if tool.Definition.Name == CloseSubagentToolName {
			if tool.TurnBehavior != ContinueTurn || tool.resultTurnBehavior == nil || strings.Contains(schema, `"finish_turn"`) || !strings.Contains(tool.Definition.Description, "always continues") {
				t.Fatalf("close_subagent must continue on its first lifecycle result without finish_turn: %#v", tool)
			}
		}
		if tool.Definition.Name != StartSubagentToolName && tool.Definition.Name != SendSubagentMessageToolName && tool.Definition.Name != CloseSubagentToolName && tool.Definition.Name != ForceCloseSubagentToolName && tool.TurnBehavior != ContinueTurn {
			t.Fatalf("subagent management tool %q turn behavior = %q, want continue", tool.Definition.Name, tool.TurnBehavior)
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
		if tool.Definition.Name == CloseSubagentToolName && (!strings.Contains(tool.Definition.Description, "cleanup, not cancellation") || !strings.Contains(tool.Definition.Description, "Never call this after") || !strings.Contains(tool.Definition.Description, "completed work") || !strings.Contains(tool.Definition.Description, "failed work") || !strings.Contains(tool.Definition.Description, "runtime rejects running")) {
			t.Fatalf("close_subagent does not describe lifecycle judgment: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == ForceCloseSubagentToolName {
			if tool.Confirmation != nil || !strings.Contains(tool.Definition.Description, "latest user message explicitly") || !strings.Contains(tool.Definition.Description, "Never choose it autonomously") || !strings.Contains(tool.Definition.Description, "discard queued child messages") {
				t.Fatalf("force_close_subagent safety contract = %#v", tool)
			}
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
		if tool.Definition.Name == CloseSubagentToolName {
			if tool.TurnBehavior != ContinueTurn || tool.resultTurnBehavior == nil {
				t.Fatalf("close behavior = (%q, %v), want dynamic bounded conflict handling", tool.TurnBehavior, tool.resultTurnBehavior != nil)
			}
			if got := tool.resultTurnBehavior(nil, json.RawMessage(`{"turn_behavior":"continue_turn"}`)); got != ContinueTurn {
				t.Fatalf("first close conflict behavior = %q, want continue", got)
			}
			if got := tool.resultTurnBehavior(nil, json.RawMessage(`{"turn_behavior":"end_turn"}`)); got != EndTurn {
				t.Fatalf("repeated close conflict behavior = %q, want end_turn", got)
			}
			continue
		}
		if tool.TurnBehavior != ContinueTurn || tool.resultTurnBehavior != nil {
			t.Fatalf("%s behavior = (%q, dynamic=%v), want static continue", tool.Definition.Name, tool.TurnBehavior, tool.resultTurnBehavior != nil)
		}
	}
}

func TestRepeatedCloseConflictEndsOnlyTheCurrentTurn(t *testing.T) {
	bridge := NewSubagentToolBridge()
	firstTurn := Invocation{SessionID: "session", TurnID: "turn-1"}
	if got := bridge.closeConflictTurnBehavior(firstTurn, "child"); got != "continue_turn" {
		t.Fatalf("first conflict = %q, want continue_turn", got)
	}
	if got := bridge.closeConflictTurnBehavior(firstTurn, "child"); got != "end_turn" {
		t.Fatalf("repeated conflict = %q, want end_turn", got)
	}
	secondTurn := Invocation{SessionID: "session", TurnID: "turn-2"}
	if got := bridge.closeConflictTurnBehavior(secondTurn, "child"); got != "continue_turn" {
		t.Fatalf("new-turn conflict = %q, want continue_turn", got)
	}
}
