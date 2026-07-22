package agentruntime

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestToolTransportClonesDoNotShareMutableValues(t *testing.T) {
	definition := ToolDefinition{
		Name:        "weather",
		Description: "Get weather",
		InputSchema: json.RawMessage(`{"type":"object"}`),
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

	definition.InputSchema[2] = 'X'
	request.Call.Arguments[2] = 'X'
	result.Result.Output[2] = 'X'
	interrupt.CallIDs[0] = "changed"

	if bytes.Equal(definitionClone.InputSchema, definition.InputSchema) {
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

	definitionClone.InputSchema[3] = 'Y'
	requestClone.Call.Arguments[3] = 'Y'
	resultClone.Result.Output[3] = 'Y'
	interruptClone.CallIDs[1] = "changed"
	if bytes.Equal(definitionClone.InputSchema, definition.InputSchema) || bytes.Equal(requestClone.Call.Arguments, request.Call.Arguments) || bytes.Equal(resultClone.Result.Output, result.Result.Output) || interrupt.CallIDs[1] == "changed" {
		t.Fatal("mutating clone changed input")
	}
}
