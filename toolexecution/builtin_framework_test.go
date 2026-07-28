package toolexecution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
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
		"HARD TURN-SCOPED LIMIT",
		"Each named skill may be loaded at most once per runtime turn",
		"trusted <runtime_turn_boundary> reminder with state=new_turn",
		"provider requests without that marker",
		"MUST NOT call load_skill for that skill again until a new <runtime_turn_boundary>",
		"Tool results, later provider steps, and continued reasoning do not reset this limit",
		"skill description in available_skills directly matches",
		"applicable instruction explicitly requires",
		"user asks to inspect",
		"Discovery-only questions",
		"Inspect the complete result",
		"runtime-managed",
		"load request succeeded",
		"A successful load from an earlier runtime turn does not satisfy a trigger in a newly marked runtime turn",
		"Every successful result uses status=loaded",
		"loads only the exact skill named",
		"load_trigger_satisfied_for",
		"satisfies only the current load trigger for that named skill",
		"does not load or satisfy a trigger for any other skill",
		"separate valid trigger",
		"instructions_in_context=true",
		"already available in the conversation context",
		"does not decide whether the turn should continue, wait, or end",
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
		!strings.Contains(schema, "description-match, explicit-requirement, or explicit-inspection trigger") ||
		!strings.Contains(schema, "MUST NOT submit a name already present in load_trigger_satisfied_for") ||
		!strings.Contains(schema, "eligible again only after a new") {
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
		result.LoadTriggerSatisfiedFor != "testing-go" ||
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
		reused.Name != "testing-go" || reused.LoadTriggerSatisfiedFor != "testing-go" ||
		reused.Message != skillLoadedMessage("testing-go", true) {
		t.Fatalf("reused skill result does not identify instructions in context: %#v", reused)
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
			if tool.Trigger != "" || tool.EndTurnOnSuccess || strings.Contains(schema, `"finish_turn"`) {
				t.Fatalf("subagent operation %q must not use static terminal behavior or legacy finish_turn: %#v", tool.Definition.Name, tool)
			}
		}
		if tool.Definition.Name == StartSubagentToolName && tool.resultTurnBehavior != nil {
			t.Fatalf("start_subagent unexpectedly has dynamic terminal behavior")
		}
		if tool.Definition.Name == SendSubagentMessageToolName && tool.resultTurnBehavior == nil {
			t.Fatalf("send_subagent_message must resolve terminal behavior from its result")
		}
		if tool.Definition.Name == StartSubagentToolName && (!strings.Contains(tool.Definition.Description, "Every successful call creates a separately addressed child") || !strings.Contains(tool.Definition.Description, "never reuses or continues an existing child") || !strings.Contains(tool.Definition.Description, "use send_subagent_message") || strings.Contains(schema, `"new_instance"`)) {
			t.Fatalf("start_subagent does not enforce create-only routing: %#v", tool.Definition)
		}
		if tool.Definition.Name == StartSubagentToolName {
			for _, expected := range []string{
				"not a status, reminder, or follow-up tool for running work",
				"wait for its automatic result",
				"explicitly choose continue_after_dispatch",
				"controls only the parent turn and never subagent concurrency",
				"Set it to false when no specific independent parent work remains",
				"subagents keep running",
				"Multiple subagents started together with false still run in parallel",
				"Set it to true only",
				"already planned",
				"never invent work to justify true",
				"use the same value on all calls",
				"all false when no parent work remains",
				"all true only for that planned independent work",
				"Never mix values",
				"any false call that accepts work ends the successful batch",
				"merely to simulate waiting",
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
				"Controls only whether the parent continues immediately",
				"Multiple subagents started together with false still run in parallel",
				"all false when no parent work remains",
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
			!strings.Contains(tool.Definition.Description, "latest result has been delivered and consumed") ||
			!strings.Contains(tool.Definition.Description, "applicable instruction or the user explicitly requires") ||
			!strings.Contains(tool.Definition.Description, "Never call while the child is running") ||
			!strings.Contains(tool.Definition.Description, "not for waiting, status checks, polling") ||
			!strings.Contains(tool.Definition.Description, "duplicate instructions") ||
			!strings.Contains(tool.Definition.Description, "accepted=false with duplicate, already_sent, or callback_pending") ||
			!strings.Contains(tool.Definition.Description, "inspect accepted, action, callback_action, must_wait_for_callback, turn_action, and instruction")) {
			t.Fatalf("send_subagent_message does not align continuation triggers and result handling: %q", tool.Definition.Description)
		}
		if tool.Definition.Name == SendSubagentMessageToolName {
			for _, expected := range []string{
				"ID of an idle incomplete or failed child whose latest result was already delivered and consumed",
				"Never use a running, completed, or closed child",
				"Do not send unrelated new work, status checks, reminders",
				`"continue_after_dispatch"`,
				`"required":["subagent_id","message","continue_after_dispatch"]`,
				"Controls only whether the parent continues immediately",
				"Set false when no specific independent parent work remains",
			} {
				if !strings.Contains(schema, expected) {
					t.Fatalf("send_subagent_message schema does not contain idle-only rule %q: %s", expected, schema)
				}
			}
		}
		if tool.Definition.Name == SendSubagentMessageToolName {
			for _, expected := range []string{
				"explicitly choose continue_after_dispatch",
				"accepted result ends the current successful tool batch automatically",
				"controls only the parent turn",
				"Set it to true only",
				"already planned",
				"successful tool batch ends automatically regardless of continue_after_dispatch",
				"recovery_exhausted",
				"continue to report the terminal failure",
				"merely to simulate waiting",
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
		if tool.Trigger != "" || tool.EndTurnOnSuccess {
			t.Fatalf("%s static terminal behavior = (trigger=%q, end_on_success=%t)", tool.Definition.Name, tool.Trigger, tool.EndTurnOnSuccess)
		}
		if tool.Definition.Name == StartSubagentToolName && tool.resultTurnBehavior != nil {
			t.Fatalf("start_subagent dynamic terminal behavior is set")
		}
		if tool.Definition.Name == SendSubagentMessageToolName && tool.resultTurnBehavior == nil {
			t.Fatalf("send_subagent_message dynamic terminal behavior is not set")
		}
	}

	tests := []struct {
		name      string
		arguments string
		output    string
		want      agentruntime.ToolTurnBehavior
	}{
		{"accepted wait", `{"continue_after_dispatch":false}`, `{"accepted":true,"action":"started"}`, agentruntime.ToolTurnEndOnSuccess},
		{"accepted continue", `{"continue_after_dispatch":true}`, `{"accepted":true,"action":"started"}`, agentruntime.ToolTurnContinue},
		{"duplicate always waits", `{"continue_after_dispatch":true}`, `{"accepted":false,"action":"duplicate"}`, agentruntime.ToolTurnEndOnSuccess},
		{"already sent always waits", `{"continue_after_dispatch":false}`, `{"accepted":false,"action":"already_sent"}`, agentruntime.ToolTurnEndOnSuccess},
		{"callback pending always waits", `{"continue_after_dispatch":true}`, `{"accepted":false,"action":"callback_pending"}`, agentruntime.ToolTurnEndOnSuccess},
		{"completed continues", `{"continue_after_dispatch":false}`, `{"accepted":false,"action":"child_completed"}`, agentruntime.ToolTurnContinue},
		{"recovery exhausted continues", `{"continue_after_dispatch":false}`, `{"accepted":false,"action":"recovery_exhausted"}`, agentruntime.ToolTurnContinue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sendSubagentMessageTurnBehavior(json.RawMessage(test.arguments), json.RawMessage(test.output))
			if got != test.want {
				t.Fatalf("turn behavior = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSendSubagentMessageRecoveryExhaustedResultReportsTerminalFailure(t *testing.T) {
	bridge := NewSubagentToolBridge()
	bridge.Bind(staticSubagentController{send: SubagentSendResult{
		Action: SubagentSendRecoveryExhausted,
		Subagent: storage.Subagent{
			ID: "child", DisplayName: "Aster", DefinitionName: "researcher", Status: storage.SubagentStatusIdle,
		},
	}})
	var sendTool Tool
	for _, tool := range bridge.Tools() {
		if tool.Definition.Name == SendSubagentMessageToolName {
			sendTool = tool
			break
		}
	}
	ctx := WithInvocation(context.Background(), Invocation{
		SessionID: "parent", TurnID: "callback-turn", CallID: "send", ToolName: SendSubagentMessageToolName,
	})
	output, err := sendTool.Handler(ctx, json.RawMessage(`{"subagent_id":"child","message":"retry","continue_after_dispatch":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var result sendSubagentMessageToolOutput
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != SubagentSendRecoveryExhausted || result.Accepted || result.Callback != "none" ||
		result.MustWait || result.TurnAction != "continue_to_report_terminal_failure" ||
		!strings.Contains(result.Instruction, "Report the terminal failure") ||
		!strings.Contains(result.Instruction, "do not retry") {
		t.Fatalf("recovery_exhausted output = %s", output)
	}
}

type staticSubagentController struct {
	send SubagentSendResult
}

func (controller staticSubagentController) Start(context.Context, string, string, string, string, string) (storage.Subagent, error) {
	return storage.Subagent{}, nil
}

func (controller staticSubagentController) List(context.Context, string, bool) ([]storage.Subagent, error) {
	return nil, nil
}

func (controller staticSubagentController) StatusFromParentTurn(context.Context, string, string, string) (SubagentStatusSnapshot, error) {
	return SubagentStatusSnapshot{}, nil
}

func (controller staticSubagentController) SendFromParentTurn(context.Context, string, string, string, string) (SubagentSendResult, error) {
	return controller.send, nil
}
