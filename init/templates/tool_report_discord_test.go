package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli"
)

func TestReportDiscordToolIsRequiredFinalizer(t *testing.T) {
	tool := newReportDiscordTool(t.TempDir())
	if tool.Definition.Name != "report_discord" || tool.Handler == nil {
		t.Fatalf("tool = %#v", tool)
	}
	if !tool.RequiredAtTurnEnd || tool.TurnBehavior != agentcli.EndTurn {
		t.Fatalf("finalizer metadata = required:%t behavior:%q", tool.RequiredAtTurnEnd, tool.TurnBehavior)
	}
	if tool.ToolOutputGuard != nil || strings.TrimSpace(tool.ToolOutputGuardPrompt) == "" {
		t.Fatalf("tool output guard = function:%v prompt:%q", tool.ToolOutputGuard != nil, tool.ToolOutputGuardPrompt)
	}
	if tool.ToolOutputGuardModel != nil {
		t.Fatalf("tool output guard model = %#v, want main-model fallback", tool.ToolOutputGuardModel)
	}
	for _, required := range []string{"arguments.message", `status is "reported"`, `"skipped"`, "call report_discord again", "Do not repeat sensitive content"} {
		if !strings.Contains(tool.ToolOutputGuardPrompt, required) {
			t.Fatalf("output guard prompt %q does not contain %q", tool.ToolOutputGuardPrompt, required)
		}
	}
	if tool.Permission != nil || tool.PermissionWithPolicy != nil || tool.Confirmation != nil {
		t.Fatal("mock report must not require admission metadata")
	}
	for _, required := range []string{"successful standalone", "Do not send conversational", "only through this final call's message argument", "report=false", "retry with corrected arguments"} {
		if !strings.Contains(tool.Definition.Description, required) {
			t.Fatalf("description %q does not contain %q", tool.Definition.Description, required)
		}
	}
	if strings.Contains(tool.Definition.Description, "report/") || strings.Contains(tool.Definition.Description, "network") {
		t.Fatalf("description = %q", tool.Definition.Description)
	}
	schema, err := json.Marshal(tool.Definition.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"message"`, `"minLength":1`, `"maxLength":2000`, `"report"`, `"type":"boolean"`, `"required":["message"]`} {
		if !strings.Contains(string(schema), expected) {
			t.Fatalf("schema %s missing %s", schema, expected)
		}
	}
}

func TestReportDiscordIsDeterministicAndDoesNotSend(t *testing.T) {
	root := t.TempDir()
	tool := newReportDiscordTool(root)
	arguments := json.RawMessage(`{"message":"Build complete."}`)
	ctx := agentcli.WithToolInvocation(context.Background(), agentcli.ToolInvocation{
		SessionID: "session-log",
		TurnID:    "turn-1",
		CallID:    "call-1",
		ToolName:  "report_discord",
	})
	first, err := tool.Handler(ctx, arguments)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tool.Handler(ctx, arguments)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("non-deterministic output: %s != %s", first, second)
	}
	var output reportDiscordResult
	if err := json.Unmarshal(first, &output); err != nil {
		t.Fatal(err)
	}
	if output.Status != "reported" {
		t.Fatalf("output = %#v", output)
	}
	encoded, err := os.ReadFile(filepath.Join(root, "report", "session-log.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []reportDiscordLogEntry
	if err := json.Unmarshal(encoded, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Sequence != 1 || entries[1].Sequence != 2 || entries[0].Message != "Build complete." || entries[1].TurnID != "turn-1" {
		t.Fatalf("log entries = %#v", entries)
	}
}

func TestReportDiscordCanSkipInternalLifecycleReport(t *testing.T) {
	root := t.TempDir()
	tool := newReportDiscordTool(root)
	ctx := agentcli.WithToolInvocation(context.Background(), agentcli.ToolInvocation{
		SessionID: "session-skip",
		TurnID:    "turn-skip",
		CallID:    "call-skip",
		ToolName:  "report_discord",
	})
	output, err := tool.Handler(ctx, json.RawMessage(`{"message":"Started subagent Robin.","report":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var result reportDiscordResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "skipped" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "report")); !os.IsNotExist(err) {
		t.Fatalf("report directory exists after skipped report: %v", err)
	}
}

func TestReportDiscordValidatesRawArguments(t *testing.T) {
	tool := newReportDiscordTool(t.TempDir())
	ctx := agentcli.WithToolInvocation(context.Background(), agentcli.ToolInvocation{
		SessionID: "session-invalid",
		TurnID:    "turn-invalid",
		CallID:    "call-invalid",
		ToolName:  "report_discord",
	})
	for _, arguments := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"message":"   "}`),
		json.RawMessage(`{"message":"ok","unknown":true}`),
		json.RawMessage(`{"message":"` + strings.Repeat("x", maximumDiscordMessageRunes+1) + `"}`),
		json.RawMessage(`{"message":"bad\u0000text"}`),
	} {
		if _, err := tool.Handler(ctx, arguments); err == nil {
			t.Fatalf("accepted invalid arguments: %s", arguments)
		} else if !strings.Contains(err.Error(), "try again") {
			t.Fatalf("validation error does not request retry: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = agentcli.WithToolInvocation(ctx, agentcli.ToolInvocation{SessionID: "session-cancel", TurnID: "turn-cancel", CallID: "call-cancel", ToolName: "report_discord"})
	if _, err := tool.Handler(ctx, json.RawMessage(`{"message":"ok"}`)); err == nil {
		t.Fatal("ignored cancelled context")
	}
}
