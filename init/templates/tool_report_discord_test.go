package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli"
	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestReportDiscordToolIsRequiredFinalizer(t *testing.T) {
	tool := newReportDiscordTool(t.TempDir())
	if tool.Definition.Name != "report_discord" || tool.Handler == nil {
		t.Fatalf("tool = %#v", tool)
	}
	if !tool.RequiredAtTurnEnd || tool.TurnBehavior != agentcli.EndTurn {
		t.Fatalf("finalizer metadata = required:%t behavior:%q", tool.RequiredAtTurnEnd, tool.TurnBehavior)
	}
	if tool.ToolCallGuard != nil || strings.TrimSpace(tool.ToolCallGuardPrompt) == "" {
		t.Fatalf("tool call guard = function:%v prompt:%q", tool.ToolCallGuard != nil, tool.ToolCallGuardPrompt)
	}
	if tool.ToolCallGuardModel != nil {
		t.Fatalf("tool call guard model = %#v, want main-model fallback", tool.ToolCallGuardModel)
	}
	for _, required := range []string{"requested report_discord tool call", "arguments.message", "as if the reporting agent performed the work itself", "useful ongoing progress is valid reportable content", "does not mention or imply delegation", "does not describe waiting", "does not promise", "A subagent is analyzing main.go", "Analyzing main.go to prepare a summary", "arguments.skipReport", "useful progress must be reported", "preserve that progress", "do not recommend skipReport", "concrete suggested message", "Never suggest an empty or null message", "do not repeat sensitive content"} {
		if !strings.Contains(tool.ToolCallGuardPrompt, required) {
			t.Fatalf("call guard prompt %q does not contain %q", tool.ToolCallGuardPrompt, required)
		}
	}
	if tool.Permission != nil || tool.PermissionWithPolicy != nil || tool.Confirmation != nil {
		t.Fatal("mock report must not require admission metadata")
	}
	for _, required := range []string{"Submit one standalone user-facing report", "final tool action of the turn", "after all other tools finish", "complete action, current progress, status, finding, or conclusion", "written directly as your own work", "Useful in-progress status is reportable", "Do not mention or imply delegation", "waiting for one", "promised later update", "Omit skipReport or set it to false", "Set skipReport=true", "no meaningful user-facing action, progress, status, finding, or conclusion", "do not use it to hide useful progress", "use the tool-result feedback", "preserve useful progress while removing internal attribution"} {
		if !strings.Contains(tool.Definition.Description, required) {
			t.Fatalf("description %q does not contain %q", tool.Definition.Description, required)
		}
	}
	for _, forbidden := range []string{"IMPORTANT", "strict tool-only output protocol", "Never emit plain assistant text", "Produce only tool calls", "subagent callback", "emit nothing after it"} {
		if strings.Contains(tool.Definition.Description, forbidden) {
			t.Fatalf("description %q contains global prompt rule %q", tool.Definition.Description, forbidden)
		}
	}
	if strings.Contains(tool.Definition.Description, "report/") || strings.Contains(tool.Definition.Description, "network") {
		t.Fatalf("description = %q", tool.Definition.Description)
	}
	schema, err := json.Marshal(tool.Definition.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"message"`, `"minLength":1`, `"maxLength":2000`, `"skipReport"`, `"type":"boolean"`, `"required":["message"]`, `useful ongoing progress is reportable`, `never mention delegation, other agents, waiting for them, or future updates`, `never use it to hide useful progress`} {
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
	second, err := tool.Handler(ctx, json.RawMessage(`{"message":"Build complete.","skipReport":false}`))
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
	output, err := tool.Handler(ctx, json.RawMessage(`{"message":"No user-facing report is necessary for this turn.","skipReport":true}`))
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

func TestReportDiscordRejectedToolCallDoesNotAppend(t *testing.T) {
	root := t.TempDir()
	tool := newReportDiscordTool(root)
	tool.ToolCallGuardPrompt = ""
	tool.ToolCallGuard = func(context.Context, agentruntime.ToolCallGuardAttempt) (agentruntime.ToolCallGuardDecision, error) {
		return agentruntime.ToolCallGuardDecision{
			Action:   agentruntime.ToolCallReject,
			Feedback: "rewrite the report as a direct user-facing result",
		}, nil
	}
	registry := toolexecution.NewRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	executor, err := toolexecution.NewExecutor(registry, 1)
	if err != nil {
		t.Fatal(err)
	}

	requests := make(chan agentruntime.ToolRequest, 1)
	results := make(chan agentruntime.ToolResultEnvelope, 1)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- executor.Run(ctx, requests, results, interrupts)
	}()
	requests <- agentruntime.ToolRequest{
		SessionID: "session-rejected",
		TurnID:    "turn-rejected",
		Call: agentruntime.ToolCall{
			CallID:    "call-rejected",
			Name:      "report_discord",
			Arguments: json.RawMessage(`{"message":"I will report back later."}`),
		},
	}
	result := <-results
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result.Result.Status != agentruntime.ToolResultFailed || !strings.Contains(result.Result.Error, "tool call rejected by guard") {
		t.Fatalf("result = %#v, want rejected tool call", result)
	}
	if _, err := os.Stat(filepath.Join(root, "report")); !os.IsNotExist(err) {
		t.Fatalf("report directory exists after rejected tool call: %v", err)
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
		json.RawMessage(`{"message":"ok","report":false}`),
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
