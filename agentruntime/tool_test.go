package agentruntime

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestToolSchemaMarshalsConstraintsAndAdditionalProperties(t *testing.T) {
	schema := ToolSchema{
		Type: "object",
		Properties: map[string]ToolSchema{
			"name":  {Type: "string", Description: "Display name", Pattern: "^[a-z]+$", MinLength: json.Number("1"), MaxLength: json.Number("32"), Enum: []json.RawMessage{json.RawMessage(`"go"`)}},
			"score": {Type: "number", Minimum: json.Number("0.125"), Maximum: json.Number("100")},
		},
		Required:             []string{"name"},
		AdditionalProperties: AdditionalPropertiesBool(false),
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	if string(object["additionalProperties"]) != "false" || string(object["required"]) != `["name"]` {
		t.Fatalf("schema = %s", encoded)
	}

	schema.AdditionalProperties = AdditionalPropertiesSchema(ToolSchema{Type: "string"})
	encoded, err = json.Marshal(schema)
	if err != nil || !json.Valid(encoded) || string(encoded) == "" {
		t.Fatalf("schema-valued additionalProperties = %s, %v", encoded, err)
	}
}

func TestRawToolSchemaValidationAndClone(t *testing.T) {
	if _, err := RawToolSchema(json.RawMessage(`[]`)); err == nil {
		t.Fatal("array raw schema accepted")
	}
	if _, err := RawToolSchema(json.RawMessage(`{"type":"array"}`)); err == nil {
		t.Fatal("non-object raw schema accepted")
	}
	schema, err := RawToolSchema(json.RawMessage(`{"type":"object","x-vendor":{"enabled":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	clone := schema.Clone()
	encoded, err := json.Marshal(clone)
	if err != nil || string(encoded) != `{"type":"object","x-vendor":{"enabled":true}}` {
		t.Fatalf("raw clone = %s, %v", encoded, err)
	}
}

func TestToolTransportClonesDoNotShareMutableValues(t *testing.T) {
	definition := ToolDefinition{
		Name:        "weather",
		Description: "Get weather",
		InputSchema: ToolSchema{Type: "object", Properties: map[string]ToolSchema{"city": {Type: "string"}}},
	}
	request := ToolRequest{
		SessionID: "session-1",
		TurnID:    "turn-1",
		Call: ToolCall{
			CallID:    "call-1",
			Name:      "weather",
			Arguments: json.RawMessage(`{"city":"Bangkok"}`),
		},
	}
	result := ToolResultEnvelope{
		SessionID: "session-1",
		TurnID:    "turn-1",
		Result: ToolResult{
			CallID: "call-1",
			Name:   "weather",
			Status: ToolResultSucceeded,
			Output: json.RawMessage(`{"temperature":30}`),
		},
	}
	interrupt := ToolInterrupt{SessionID: "session-1", TurnID: "turn-1", CallIDs: []string{"call-1", "call-2"}, Reason: "cancelled"}

	definitionClone := cloneToolDefinition(definition)
	requestClone := cloneToolRequest(request)
	resultClone := cloneToolResultEnvelope(result)
	interruptClone := cloneToolInterrupt(interrupt)

	definition.InputSchema.Properties["city"] = ToolSchema{Type: "number"}
	request.Call.Arguments[2] = 'X'
	result.Result.Output[2] = 'X'
	interrupt.CallIDs[0] = "changed"

	if definitionClone.InputSchema.Properties["city"].Type == definition.InputSchema.Properties["city"].Type {
		t.Fatal("ToolDefinition clone shares InputSchema")
	}
	if bytes.Equal(requestClone.Call.Arguments, request.Call.Arguments) {
		t.Fatal("ToolRequest clone shares Call.Arguments")
	}
	if bytes.Equal(resultClone.Result.Output, result.Result.Output) {
		t.Fatal("ToolResultEnvelope clone shares Result.Output")
	}
	if interruptClone.CallIDs[0] != "call-1" {
		t.Fatalf("ToolInterrupt clone CallIDs = %v, want independent copy", interruptClone.CallIDs)
	}

	definitionClone.InputSchema.Properties["city"] = ToolSchema{Type: "boolean"}
	requestClone.Call.Arguments[3] = 'Y'
	resultClone.Result.Output[3] = 'Y'
	interruptClone.CallIDs[1] = "changed"
	if definitionClone.InputSchema.Properties["city"].Type == definition.InputSchema.Properties["city"].Type || string(requestClone.Call.Arguments) == string(request.Call.Arguments) || string(resultClone.Result.Output) == string(result.Result.Output) || interrupt.CallIDs[1] == "changed" {
		t.Fatal("mutating clone changed input")
	}
}
