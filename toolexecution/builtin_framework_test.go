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
	for _, expected := range []string{
		"after a valid load trigger",
		"skill description in available_skills directly matches",
		"applicable instruction explicitly requires",
		"user asks to inspect",
		"Discovery-only questions",
		"Inspect the complete result",
		"always call this tool",
		"runtime-managed",
		"already_loaded",
	} {
		if !strings.Contains(tool.Definition.Description, expected) {
			t.Fatalf("load_skill description does not contain %q: %q", expected, tool.Definition.Description)
		}
	}
	schema := string(marshaledToolSchema(t, tool.Definition.InputSchema))
	if !strings.Contains(schema, `"minLength":1`) ||
		!strings.Contains(schema, "description-match, explicit-requirement, or explicit-inspection trigger") {
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
	if result.Status != "loaded" || result.Instructions != "Run the Go tests." {
		t.Fatalf("skill result = %s", output)
	}
}

func TestSubagentToolBridgeOwnsCompleteReservedCatalog(t *testing.T) {
	tools := NewSubagentToolBridge().Tools()
	if len(tools) != 2 {
		t.Fatalf("subagent tool count = %d, want 2", len(tools))
	}
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if !IsSubagentToolName(tool.Definition.Name) || tool.Handler == nil || !json.Valid(marshaledToolSchema(t, tool.Definition.InputSchema)) {
			t.Fatalf("invalid subagent built-in %q", tool.Definition.Name)
		}
		if tool.Definition.Name == StartSubagentToolName && !strings.Contains(tool.Definition.Description, "simple self-contained work do not trigger this tool by themselves") {
			t.Fatalf("start_subagent does not discourage unnecessary delegation: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "after a valid delegation trigger") ||
			!strings.Contains(tool.Definition.Description, "definition description directly matches") ||
			!strings.Contains(tool.Definition.Description, "applicable instruction or the user explicitly requires") ||
			!strings.Contains(tool.Definition.Description, "Topic overlap, discovery-only questions, and simple self-contained work do not trigger this tool by themselves") ||
			!strings.Contains(tool.Definition.Description, "explicit requirement remains a valid trigger") ||
			!strings.Contains(tool.Definition.Description, "selection metadata, not proof that work started")) {
			t.Fatalf("start_subagent does not align description-match and explicit-requirement triggers: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "ordinary lookup or research") || !strings.Contains(tool.Definition.Description, "Multiple starts")) {
			t.Fatalf("start_subagent does not describe sequential default and intentional fanout: %q", tool.Definition.Description)
		}
		schema := string(marshaledToolSchema(t, tool.Definition.InputSchema))
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "accepted=true means dispatched") || strings.Contains(schema, `"background"`)) {
			t.Fatalf("start_subagent does not advertise its asynchronous default: %#v", tool.Definition)
		}
		if tool.Definition.Name == StartSubagentToolName || tool.Definition.Name == SendSubagentMessageToolName {
			if tool.Trigger != "" || tool.EndTurnOnSuccess || tool.resultTurnBehavior != nil || strings.Contains(schema, `"finish_turn"`) {
				t.Fatalf("subagent operation %q must not use static terminal behavior or legacy finish_turn: %#v", tool.Definition.Name, tool)
			}
		}
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "Every successful call creates a separately addressed child") || !strings.Contains(tool.Definition.Description, "never reuses or continues an existing child") || !strings.Contains(tool.Definition.Description, "use send_subagent_message") || strings.Contains(schema, `"new_instance"`)) {
			t.Fatalf("start_subagent does not enforce create-only routing: %#v", tool.Definition)
		}
		if tool.Definition.Name == StartSubagentToolName {
			for _, expected := range []string{
				"not a status, reminder, or follow-up tool for running work",
				"wait for its automatic callback",
				"explicitly choose continue_after_dispatch",
				"Set it to false",
				"successful result with a pending callback",
				"whole tool batch succeeds",
				"without another provider step or assistant content",
				"Set it to true only",
				"already planned",
				"never invent work to justify true",
				"use the same value on all calls",
				"all false to wait",
				"all true to continue",
				"Never mix values",
				"any false call that returns a pending callback ends the successful batch",
			} {
				if !strings.Contains(tool.Definition.Description, expected) {
					t.Fatalf("start_subagent description does not contain turn-choice rule %q: %q", expected, tool.Definition.Description)
				}
			}
			for _, expected := range []string{
				"result expected from the new child",
				"continue an existing child",
				`"continue_after_dispatch"`,
				`"required":["name","message","continue_after_dispatch"]`,
				"Required turn choice made before dispatch",
				"all false to wait after the batch",
				"all true to continue independent parent work",
			} {
				if !strings.Contains(schema, expected) {
					t.Fatalf("start_subagent schema does not contain turn-choice rule %q: %s", expected, schema)
				}
			}
		}
		if tool.Definition.Name == SendSubagentMessageToolName {
			for _, expected := range []string{
				"idle incomplete or failed child only",
				"Never reuse a completed child",
				"child_completed also means no dispatch",
			} {
				if !strings.Contains(tool.Definition.Description, expected) {
					t.Fatalf("send_subagent_message description does not contain lifecycle rule %q: %q", expected, tool.Definition.Description)
				}
			}
			for _, expected := range []string{
				"idle incomplete or failed child",
				"Never use a running, completed, or closed child",
				"Do not send unrelated new work",
			} {
				if !strings.Contains(schema, expected) {
					t.Fatalf("send_subagent_message schema does not contain lifecycle rule %q: %s", expected, schema)
				}
			}
		}
		if tool.Definition.Name == SendSubagentMessageToolName && (!strings.Contains(tool.Definition.Description, "focused follow-up") || !strings.Contains(tool.Definition.Description, "never completed") || !strings.Contains(tool.Definition.Description, "arrives automatically")) {
			t.Fatalf("send_subagent_message does not describe callback-driven follow-up: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == SendSubagentMessageToolName && (!strings.Contains(tool.Definition.Description, "idle incomplete or failed child only") ||
			!strings.Contains(tool.Definition.Description, "latest callback has been consumed") ||
			!strings.Contains(tool.Definition.Description, "applicable instruction or the user explicitly requires") ||
			!strings.Contains(tool.Definition.Description, "Never call while the child is running") ||
			!strings.Contains(tool.Definition.Description, "not for waiting, status checks, polling") ||
			!strings.Contains(tool.Definition.Description, "duplicate instructions") ||
			!strings.Contains(tool.Definition.Description, "accepted=false with duplicate, already_sent, or callback_pending") ||
			!strings.Contains(tool.Definition.Description, "inspect accepted, action, callback_action, must_wait_for_callback, and instruction")) {
			t.Fatalf("send_subagent_message does not align continuation triggers and result handling: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == SendSubagentMessageToolName {
			for _, expected := range []string{
				"ID of an idle incomplete or failed child whose latest callback was already received and consumed",
				"Never use a running, completed, or closed child",
				"Do not send unrelated new work, status checks, reminders",
			} {
				if !strings.Contains(schema, expected) {
					t.Fatalf("send_subagent_message schema does not contain idle-only rule %q: %s", expected, schema)
				}
			}
		}
		if (tool.Definition.Name == StartSubagentToolName || tool.Definition.Name == SendSubagentMessageToolName) && (!strings.Contains(tool.Definition.Description, "provider boundary") || !strings.Contains(tool.Definition.Description, "callback continuation turn")) {
			t.Fatalf("asynchronous dispatch tool %q does not describe automatic callback delivery: %q", tool.Definition.Name, tool.Definition.Description)
		}
		if tool.Definition.Name == SendSubagentMessageToolName {
			for _, expected := range []string{
				"work already planned before dispatch",
				"outside the delegated task",
				"independent of its callback",
				"end the turn immediately without assistant content or another tool call",
				"do not narrate waiting",
				"response or delivery tool",
			} {
				if !strings.Contains(tool.Definition.Description, expected) {
					t.Fatalf("asynchronous dispatch tool %q does not contain post-dispatch rule %q: %q", tool.Definition.Name, expected, tool.Definition.Description)
				}
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
	if seen[ListSubagentsToolName] || seen[SubagentStatusToolName] {
		t.Fatalf("inspection tools remain model-facing: %#v", seen)
	}
}

func TestSubagentToolTurnBehavior(t *testing.T) {
	for _, tool := range NewSubagentToolBridge().Tools() {
		if tool.Trigger != "" || tool.EndTurnOnSuccess || tool.resultTurnBehavior != nil {
			t.Fatalf("%s static terminal behavior = (trigger=%q, end_on_success=%t, dynamic=%v), want handler-controlled behavior", tool.Definition.Name, tool.Trigger, tool.EndTurnOnSuccess, tool.resultTurnBehavior != nil)
		}
	}
}
