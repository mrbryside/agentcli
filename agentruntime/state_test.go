package agentruntime

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/mrbryside/agentcli/provider"
)

func TestStateAppendsWithoutChangingPriorState(t *testing.T) {
	first := AgentEvent{Sequence: 1, Type: RunStarted, SessionID: "session-1", TurnID: "turn-1"}
	second := AgentEvent{Sequence: 2, Type: ProviderEventReceived, SessionID: "session-1", TurnID: "turn-1"}

	initial := EmptyState()
	withFirst := State(initial, first)
	withSecond := State(withFirst, second)

	if got := Events(initial); len(got) != 0 {
		t.Fatalf("initial events = %#v, want empty", got)
	}
	if got := Events(withFirst); len(got) != 1 || got[0].Sequence != 1 {
		t.Fatalf("first state = %#v, want only sequence 1", got)
	}
	if got := Events(withSecond); len(got) != 2 || got[0].Sequence != 1 || got[1].Sequence != 2 {
		t.Fatalf("second state sequence = %#v, want [1 2]", got)
	}
}

func TestEventsClonesEveryMutableNestedValue(t *testing.T) {
	message := &Message{ToolCalls: []ToolCall{{CallID: "call-1", Name: "weather", Arguments: json.RawMessage(`{"city":"Bangkok"}`)}}}
	providerEvent := provider.StreamEvent{
		Type: provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "provider-call-1", Name: "weather", Arguments: map[string]any{"city": "Bangkok", "nested": map[string]any{"unit": "C"}},
		}}}},
	}
	request := &ToolRequest{Call: ToolCall{CallID: "call-1", Arguments: json.RawMessage(`{"city":"Bangkok"}`)}}
	toolResult := &ToolResultEnvelope{Result: ToolResult{CallID: "call-1", Output: json.RawMessage(`{"temperature":30}`)}}
	runResult := &RunResult{ToolResults: []ToolResult{{CallID: "call-1", Output: json.RawMessage(`{"temperature":30}`)}}}

	state := State(EmptyState(), AgentEvent{
		Sequence: 1, Message: message, ProviderEvent: providerEvent, ToolRequest: request, ToolResult: toolResult, Result: runResult,
	})
	history := Events(state)
	history[0].Message.ToolCalls[0].Arguments[2] = 'X'
	history[0].ProviderEvent.Payload.(provider.StreamCompletedPayload).Result.CompletedTools[0].Arguments["nested"].(map[string]any)["unit"] = "F"
	history[0].ToolRequest.Call.Arguments[2] = 'X'
	history[0].ToolResult.Result.Output[2] = 'X'
	history[0].Result.ToolResults[0].Output[2] = 'X'

	got := Events(state)[0]
	if bytes.Equal(got.Message.ToolCalls[0].Arguments, history[0].Message.ToolCalls[0].Arguments) {
		t.Fatal("message tool-call arguments share storage")
	}
	if unit := got.ProviderEvent.Payload.(provider.StreamCompletedPayload).Result.CompletedTools[0].Arguments["nested"].(map[string]any)["unit"]; unit != "C" {
		t.Fatalf("provider payload nested map = %v, want C", unit)
	}
	if bytes.Equal(got.ToolRequest.Call.Arguments, history[0].ToolRequest.Call.Arguments) {
		t.Fatal("tool request arguments share storage")
	}
	if bytes.Equal(got.ToolResult.Result.Output, history[0].ToolResult.Result.Output) {
		t.Fatal("tool result output shares storage")
	}
	if bytes.Equal(got.Result.ToolResults[0].Output, history[0].Result.ToolResults[0].Output) {
		t.Fatal("run result output shares storage")
	}

	message.ToolCalls[0].Arguments[3] = 'Y'
	providerEvent.Payload.(provider.StreamCompletedPayload).Result.CompletedTools[0].Arguments["city"] = "Paris"
	request.Call.Arguments[3] = 'Y'
	toolResult.Result.Output[3] = 'Y'
	runResult.ToolResults[0].Output[3] = 'Y'
	got = Events(state)[0]
	if bytes.Equal(got.Message.ToolCalls[0].Arguments, message.ToolCalls[0].Arguments) ||
		got.ProviderEvent.Payload.(provider.StreamCompletedPayload).Result.CompletedTools[0].Arguments["city"] != "Bangkok" ||
		bytes.Equal(got.ToolRequest.Call.Arguments, request.Call.Arguments) ||
		bytes.Equal(got.ToolResult.Result.Output, toolResult.Result.Output) ||
		bytes.Equal(got.Result.ToolResults[0].Output, runResult.ToolResults[0].Output) {
		t.Fatal("state retained caller-owned mutable values")
	}
}
