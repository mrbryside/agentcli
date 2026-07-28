package agentcli

import (
	"strings"
	"testing"
)

func TestParseTaskFinalResult(t *testing.T) {
	contract := &AgentResultContract{
		MessageField: "message",
		Metadata: map[string]AgentResultMetadataField{
			"requires_requester_reply": {Type: "boolean", Required: true},
			"source":                   {Type: "string"},
		},
	}
	for _, test := range []struct {
		name       string
		definition SubagentDefinition
		text       string
		wantOutput string
		wantMeta   map[string]any
		wantErr    string
	}{
		{name: "ordinary final text", text: "A normal final response.", wantOutput: "A normal final response."},
		{name: "valid contracted JSON", definition: SubagentDefinition{Result: contract}, text: `{"message":"Need clarification.","requires_requester_reply":true,"source":"discord"}`, wantOutput: "Need clarification.", wantMeta: map[string]any{"requires_requester_reply": true, "source": "discord"}},
		{name: "missing message", definition: SubagentDefinition{Result: contract}, text: `{"requires_requester_reply":true}`, wantErr: "message"},
		{name: "missing required metadata", definition: SubagentDefinition{Result: contract}, text: `{"message":"Done"}`, wantErr: "requires_requester_reply"},
		{name: "wrong message type", definition: SubagentDefinition{Result: contract}, text: `{"message":null,"requires_requester_reply":true}`, wantErr: "message"},
		{name: "empty message", definition: SubagentDefinition{Result: contract}, text: `{"message":"  ","requires_requester_reply":true}`, wantErr: "empty"},
		{name: "wrong metadata type", definition: SubagentDefinition{Result: contract}, text: `{"message":"Done","requires_requester_reply":null}`, wantErr: "requires_requester_reply"},
		{name: "unknown metadata", definition: SubagentDefinition{Result: contract}, text: `{"message":"Done","requires_requester_reply":false,"unexpected":true}`, wantErr: "unknown"},
		{name: "malformed JSON", definition: SubagentDefinition{Result: contract}, text: `{"message":`, wantErr: "JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := parseTaskFinalResult(test.definition, test.text)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Output != test.wantOutput {
				t.Fatalf("output = %q, want %q", result.Output, test.wantOutput)
			}
			if !sameMetadata(result.Metadata, test.wantMeta) {
				t.Fatalf("metadata = %#v, want %#v", result.Metadata, test.wantMeta)
			}
		})
	}
}

func sameMetadata(got, want map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			return false
		}
	}
	return true
}
