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
		schema := string(marshaledToolSchema(t, tool.Definition.InputSchema))
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
		if tool.Definition.Name == StartSubagentToolName {
			for _, expected := range []string{
				"Start one new subagent",
				"never continues an existing subagent",
				"use send_subagent_message",
				"independent and useful in parallel",
				"accepted, action, main_agent_action, and instruction",
				"accepted=true means work started, not completed",
				"<subagent_result>",
				"status checks, reminders, polling, or waiting",
			} {
				if !strings.Contains(tool.Definition.Description, expected) {
					t.Fatalf("start_subagent description does not contain turn-choice rule %q: %q", expected, tool.Definition.Description)
				}
			}
			for _, expected := range []string{
				`"continue_main_agent"`,
				`"required":["name","message","continue_main_agent"]`,
				"main agent should stop",
				"independent main-agent work",
				"does not control parallel subagents",
				"same value",
			} {
				if !strings.Contains(schema, expected) {
					t.Fatalf("start_subagent schema does not contain turn-choice rule %q: %s", expected, schema)
				}
			}
		}
		if tool.Definition.Name == SendSubagentMessageToolName {
			for _, expected := range []string{
				"one focused follow-up",
				"exact idle subagent",
				"incomplete or failed <subagent_result>",
				"Never send to running or completed work",
				"accepted, action, main_agent_action, and instruction",
				"next subagent turn started, not completed",
			} {
				if !strings.Contains(tool.Definition.Description, expected) {
					t.Fatalf("send_subagent_message description does not contain lifecycle rule %q: %q", expected, tool.Definition.Description)
				}
			}
			for _, expected := range []string{
				"Exact id of an idle incomplete or failed subagent",
				"One focused follow-up",
				"continue_main_agent",
				"main agent should stop",
			} {
				if !strings.Contains(schema, expected) {
					t.Fatalf("send_subagent_message schema does not contain lifecycle rule %q: %s", expected, schema)
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

func TestSubagentToolSummaryUsesExplicitIdentityFields(t *testing.T) {
	encoded, err := json.Marshal(summarizeSubagent(storage.Subagent{
		ID:                    "subagent",
		SubagentSessionID:     "subagent-session",
		CurrentSubagentTurnID: "subagent-turn",
	}))
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	for _, expected := range []string{
		`"subagent_session_id":"subagent-session"`,
		`"current_subagent_turn_id":"subagent-turn"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("subagent summary missing %q: %s", expected, output)
		}
	}
	for _, legacy := range []string{`"session_id"`, `"current_turn_id"`} {
		if strings.Contains(output, legacy) {
			t.Fatalf("subagent summary contains ambiguous field %q: %s", legacy, output)
		}
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
		{"accepted wait", `{"continue_main_agent":false}`, `{"accepted":true,"action":"started"}`, agentruntime.ToolTurnEndOnSuccess},
		{"accepted continue", `{"continue_main_agent":true}`, `{"accepted":true,"action":"started"}`, agentruntime.ToolTurnContinue},
		{"duplicate always waits", `{"continue_main_agent":true}`, `{"accepted":false,"action":"duplicate"}`, agentruntime.ToolTurnEndOnSuccess},
		{"already sent always waits", `{"continue_main_agent":false}`, `{"accepted":false,"action":"already_sent"}`, agentruntime.ToolTurnEndOnSuccess},
		{"callback pending always waits", `{"continue_main_agent":true}`, `{"accepted":false,"action":"result_pending"}`, agentruntime.ToolTurnEndOnSuccess},
		{"completed continues", `{"continue_main_agent":false}`, `{"accepted":false,"action":"subagent_completed"}`, agentruntime.ToolTurnContinue},
		{"recovery exhausted continues", `{"continue_main_agent":false}`, `{"accepted":false,"action":"recovery_exhausted"}`, agentruntime.ToolTurnContinue},
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
	output, err := sendTool.Handler(ctx, json.RawMessage(`{"subagent_id":"child","message":"retry","continue_main_agent":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var result sendSubagentMessageToolOutput
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != SubagentSendRecoveryExhausted || result.Accepted || result.ResultDelivery != "none" ||
		result.MainAgentAction != "report_terminal_failure" ||
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

func (controller staticSubagentController) StatusFromMainAgentTurn(context.Context, string, string, string) (SubagentStatusSnapshot, error) {
	return SubagentStatusSnapshot{}, nil
}

func (controller staticSubagentController) SendFromMainAgentTurn(context.Context, string, string, string, string) (SubagentSendResult, error) {
	return controller.send, nil
}
