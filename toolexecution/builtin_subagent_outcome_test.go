package toolexecution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSubagentOutcomeToolValidatesSemanticCompletion(t *testing.T) {
	tool := NewSubagentOutcomeTool()
	if tool.Definition.Name != SubagentOutcomeToolName || tool.Handler == nil || !json.Valid(marshaledToolSchema(t, tool.Definition.InputSchema)) {
		t.Fatalf("invalid outcome tool: %#v", tool.Definition)
	}
	for _, expected := range []string{
		"exactly once after domain work and before the final assistant answer",
		"authoritative parent callback",
		"completed only when every required part",
		"no next_step",
		"include one concrete required next_step",
		"Runtime/provider failure is handled separately",
		"If unsure, report incomplete",
		"do not call this tool or repeat domain work again",
	} {
		if !strings.Contains(tool.Definition.Description, expected) {
			t.Fatalf("report_subagent_outcome description does not contain %q: %q", expected, tool.Definition.Description)
		}
	}
	schema := string(marshaledToolSchema(t, tool.Definition.InputSchema))
	for _, expected := range []string{
		`"enum":["completed","incomplete"]`,
		`"minLength":1`,
		"required only when status is incomplete and forbidden when completed",
	} {
		if !strings.Contains(schema, expected) {
			t.Fatalf("report_subagent_outcome schema does not contain %q: %s", expected, schema)
		}
	}
	for _, test := range []struct {
		name      string
		arguments string
		want      SubagentOutcomeStatus
		wantError bool
	}{
		{name: "completed", arguments: `{"status":"completed","summary":"All work is resolved."}`, want: SubagentOutcomeCompleted},
		{name: "incomplete", arguments: `{"status":"incomplete","summary":"Need confirmation.","next_step":"Ask the user to confirm."}`, want: SubagentOutcomeIncomplete},
		{name: "completed with next step", arguments: `{"status":"completed","summary":"Done.","next_step":"Do more."}`, wantError: true},
		{name: "incomplete without next step", arguments: `{"status":"incomplete","summary":"Not done."}`, wantError: true},
		{name: "unknown", arguments: `{"status":"maybe","summary":"Unsure."}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := tool.Handler(context.Background(), json.RawMessage(test.arguments))
			if test.wantError {
				if err == nil {
					t.Fatalf("error = nil, output = %s", output)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := ParseSubagentOutcome(output)
			if err != nil || outcome.Status != test.want {
				t.Fatalf("outcome = %#v, err = %v", outcome, err)
			}
		})
	}
}
