package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli"
)

func TestReportDiscordToolIsRequiredFinalizer(t *testing.T) {
	tool := newReportDiscordTool()
	if tool.Definition.Name != "report_discord" || tool.Handler == nil {
		t.Fatalf("tool = %#v", tool)
	}
	if !tool.RequiredAtTurnEnd || tool.TurnBehavior != agentcli.EndTurn {
		t.Fatalf("finalizer metadata = required:%t behavior:%q", tool.RequiredAtTurnEnd, tool.TurnBehavior)
	}
	if tool.Permission != nil || tool.PermissionWithPolicy != nil || tool.Confirmation != nil {
		t.Fatal("mock report must not require admission metadata")
	}
	if !strings.Contains(tool.Definition.Description, "never performs network I/O") || !strings.Contains(tool.Definition.Description, "complete user-facing response") {
		t.Fatalf("description = %q", tool.Definition.Description)
	}
	schema, err := json.Marshal(tool.Definition.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"message"`, `"minLength":1`, `"maxLength":2000`, `"required":["message"]`} {
		if !strings.Contains(string(schema), expected) {
			t.Fatalf("schema %s missing %s", schema, expected)
		}
	}
}

func TestReportDiscordIsDeterministicAndDoesNotSend(t *testing.T) {
	arguments := json.RawMessage(`{"message":"Build complete."}`)
	first, err := reportDiscord(context.Background(), arguments)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reportDiscord(context.Background(), arguments)
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
	if output.Status != "simulated" || output.Destination != reportDiscordDestination || output.Message != "Build complete." || output.CharacterCount != 15 || output.NetworkCalled {
		t.Fatalf("output = %#v", output)
	}
}

func TestReportDiscordValidatesRawArguments(t *testing.T) {
	for _, arguments := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"message":"   "}`),
		json.RawMessage(`{"message":"ok","unknown":true}`),
		json.RawMessage(`{"message":"` + strings.Repeat("x", maximumDiscordMessageRunes+1) + `"}`),
		json.RawMessage(`{"message":"bad\u0000text"}`),
	} {
		if _, err := reportDiscord(context.Background(), arguments); err == nil {
			t.Fatalf("accepted invalid arguments: %s", arguments)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reportDiscord(ctx, json.RawMessage(`{"message":"ok"}`)); err == nil {
		t.Fatal("ignored cancelled context")
	}
}
