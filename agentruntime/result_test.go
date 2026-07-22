package agentruntime

import (
	"bytes"
	"errors"
	"testing"

	"harness-api/provider"
)

func TestResultRequiresTerminalEvent(t *testing.T) {
	if _, err := Result([]AgentEvent{{Type: RunStarted}}); !errors.Is(err, ErrRunNotDone) {
		t.Fatalf("Result() error = %v, want ErrRunNotDone", err)
	}
}

func TestResultPreservesFailureAndInterruption(t *testing.T) {
	cause := errors.New("provider unavailable")
	if _, err := Result([]AgentEvent{{Type: RunFailed, Error: cause}}); !errors.Is(err, cause) {
		t.Fatalf("failed result error = %v, want cause %v", err, cause)
	}
	if _, err := Result([]AgentEvent{{Type: AgentInterrupted}}); !errors.Is(err, ErrRunInterrupted) {
		t.Fatalf("interrupted result error = %v, want ErrRunInterrupted", err)
	}
}

func TestResultUsesFinalRoundAndRequestOrderedToolResults(t *testing.T) {
	events := []AgentEvent{
		{SessionID: "session-1", TurnID: "turn-1", Type: ToolCallRequested, ToolRequest: &ToolRequest{Call: ToolCall{CallID: "first", Name: "first"}}},
		{SessionID: "session-1", TurnID: "turn-1", Type: ToolCallRequested, ToolRequest: &ToolRequest{Call: ToolCall{CallID: "second", Name: "second"}}},
		{SessionID: "session-1", TurnID: "turn-1", Type: ToolResultReceived, ToolResult: &ToolResultEnvelope{Result: ToolResult{CallID: "second", Name: "second", Status: ToolResultSucceeded, Output: []byte(`{"order":2}`)}}},
		{SessionID: "session-1", TurnID: "turn-1", Type: ToolResultReceived, ToolResult: &ToolResultEnvelope{Result: ToolResult{CallID: "first", Name: "first", Status: ToolResultSucceeded, Output: []byte(`{"order":1}`)}}},
		{SessionID: "session-1", TurnID: "turn-1", Type: ProviderEventReceived, ProviderEvent: provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "intermediate", Reasoning: "first", Finished: true}}}},
		{SessionID: "session-1", TurnID: "turn-1", Type: ProviderEventReceived, ProviderEvent: provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "final", Reasoning: "last", Finished: true}}}},
		{SessionID: "session-1", TurnID: "turn-1", Type: RunCompleted},
	}

	result, err := Result(events)
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if result.SessionID != "session-1" || result.TurnID != "turn-1" || result.Content != "final" || result.Reasoning != "last" || result.Steps != 2 || !result.Finished {
		t.Fatalf("Result() = %#v, want final completed result", result)
	}
	if len(result.ToolResults) != 2 || result.ToolResults[0].CallID != "first" || result.ToolResults[1].CallID != "second" {
		t.Fatalf("ToolResults = %#v, want request order [first second]", result.ToolResults)
	}

	result.ToolResults[0].Output[2] = 'X'
	again, err := Result(events)
	if err != nil || bytes.Equal(again.ToolResults[0].Output, result.ToolResults[0].Output) {
		t.Fatalf("Result returned shared mutable values: %#v, %v", again, err)
	}
}
