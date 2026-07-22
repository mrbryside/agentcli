package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestConfirmationDemoToolDescribesAndExecutesHarmlessAction(t *testing.T) {
	tool, err := newConfirmationDemoTool()
	if err != nil {
		t.Fatal(err)
	}
	if tool.Definition.Name != "confirm_demo" || tool.Confirmation == nil || tool.Handler == nil {
		t.Fatalf("invalid demo tool: %#v", tool)
	}
	if tool.Permission != nil || tool.PermissionWithPolicy != nil {
		t.Fatal("confirmation demo must not require a permission decision")
	}

	arguments := json.RawMessage("{\"action\":\"prepare   a mock release\\nannouncement\"}")
	description, err := tool.Confirmation(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if description.Title != "Confirm mock action" || description.Message != "Run this harmless mock action?" || description.Details != "Action: prepare a mock release announcement" {
		t.Fatalf("description = %#v", description)
	}

	output, err := tool.Handler(context.Background(), arguments)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status               string `json:"status"`
		Action               string `json:"action"`
		ChangedExternalState bool   `json:"changed_external_state"`
		Message              string `json:"message"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Action != "prepare a mock release announcement" || result.ChangedExternalState || !strings.Contains(result.Message, "no external state changed") {
		t.Fatalf("result = %s", output)
	}
}

func TestConfirmationDemoToolRejectsUnsafeDisplayInput(t *testing.T) {
	tool, err := newConfirmationDemoTool()
	if err != nil {
		t.Fatal(err)
	}
	tests := []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"action":"   "}`),
		json.RawMessage(`{"action":"ok","unknown":true}`),
		json.RawMessage(`{"action":"` + strings.Repeat("x", maximumConfirmationDemoActionLength+1) + `"}`),
	}
	for _, arguments := range tests {
		if _, err := tool.Confirmation(arguments); err == nil {
			t.Fatalf("confirmation accepted %s", arguments)
		}
		if _, err := tool.Handler(context.Background(), arguments); err == nil {
			t.Fatalf("handler accepted %s", arguments)
		}
	}
}

func TestConfirmationDemoToolHonorsCancelledContext(t *testing.T) {
	tool, err := newConfirmationDemoTool()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Handler(ctx, json.RawMessage(`{"action":"mock action"}`)); err == nil {
		t.Fatal("handler ignored cancelled context")
	}
}
