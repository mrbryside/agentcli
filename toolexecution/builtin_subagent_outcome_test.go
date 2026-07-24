package toolexecution

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSubagentOutcomeToolValidatesSemanticCompletion(t *testing.T) {
	tool := NewSubagentOutcomeTool()
	if tool.Definition.Name != SubagentOutcomeToolName || tool.Handler == nil || !json.Valid(marshaledToolSchema(t, tool.Definition.InputSchema)) {
		t.Fatalf("invalid outcome tool: %#v", tool.Definition)
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
