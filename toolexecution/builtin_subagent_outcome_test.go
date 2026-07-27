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
		"omit next_step and error",
		"one concrete non-empty next_step",
		"terminal error prevents",
		"actual non-empty error",
		"never invent recovery work",
		"If unsure whether work is resolved, report incomplete",
		"do not call this tool or repeat domain work again",
	} {
		if !strings.Contains(tool.Definition.Description, expected) {
			t.Fatalf("report_subagent_outcome description does not contain %q: %q", expected, tool.Definition.Description)
		}
	}
	schema := string(marshaledToolSchema(t, tool.Definition.InputSchema))
	for _, expected := range []string{
		`"const":"completed"`,
		`"const":"incomplete"`,
		`"const":"failed"`,
		`"minLength":1`,
		`"required":["status","summary","next_step"]`,
		`"required":["status","summary","error"]`,
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
		{name: "failed", arguments: `{"status":"failed","summary":"The operation cannot continue.","error":"Discord API is unavailable."}`, want: SubagentOutcomeFailed},
		{name: "completed with next step", arguments: `{"status":"completed","summary":"Done.","next_step":"Do more."}`, wantError: true},
		{name: "completed with error", arguments: `{"status":"completed","summary":"Done.","error":"Unexpected."}`, wantError: true},
		{name: "incomplete without next step", arguments: `{"status":"incomplete","summary":"Not done."}`, wantError: true},
		{name: "incomplete with error", arguments: `{"status":"incomplete","summary":"Not done.","next_step":"Retry later.","error":"Unexpected."}`, wantError: true},
		{name: "failed without error", arguments: `{"status":"failed","summary":"Cannot continue."}`, wantError: true},
		{name: "failed with next step", arguments: `{"status":"failed","summary":"Cannot continue.","error":"Terminal failure.","next_step":"Invent recovery."}`, wantError: true},
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
