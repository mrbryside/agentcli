package provider

import (
	"errors"
	"testing"
)

func TestResultAggregatesContentReasoningAndToolArguments(t *testing.T) {
	state := EmptyState()
	events := []StreamEvent{
		{Type: ContentReceived, Content: "answer"},
		{Type: ReasoningReceived, Reasoning: "thinking"},
		{Type: ToolCallStarted, Tool: &ToolEvent{Index: 0, ID: "call_1", Type: "function", Name: "search"}},
		{Type: ToolArgumentsReceived, Tool: &ToolEvent{Index: 0, Arguments: `{"query":"go`}},
		{Type: ToolArgumentsReceived, Tool: &ToolEvent{Index: 0, Arguments: `lang"}`}},
		{Type: ToolCallCompleted, Tool: &ToolEvent{Index: 0}},
		{Type: StreamCompleted, Payload: StreamCompletedPayload{}},
	}
	for _, event := range events {
		state = State(state, event)
	}

	result, err := Result(Events(state))
	if err != nil {
		t.Fatalf("Result returned error: %v", err)
	}
	if result.Content != "answer" || result.Reasoning != "thinking" || !result.Finished {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(result.CompletedTools) != 1 {
		t.Fatalf("completed tools = %#v", result.CompletedTools)
	}
	if result.CompletedTools[0].Arguments["query"] != "golang" {
		t.Fatalf("tool arguments = %#v", result.CompletedTools[0].Arguments)
	}
}

func TestResultReturnsNotDoneBeforeTerminalEvent(t *testing.T) {
	_, err := Result([]StreamEvent{{Type: ContentReceived, Content: "partial"}})
	if !errors.Is(err, ErrStreamNotDone) {
		t.Fatalf("error = %v, want ErrStreamNotDone", err)
	}
}

func TestResultPropagatesStreamFailure(t *testing.T) {
	want := errors.New("connection lost")
	_, err := Result([]StreamEvent{{
		Type:    StreamFailed,
		Error:   want,
		Payload: StreamFailedPayload{Error: want},
	}})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestResultRejectsMalformedToolArguments(t *testing.T) {
	_, err := Result([]StreamEvent{
		{Type: ToolCallStarted, Tool: &ToolEvent{Index: 0, ID: "call_1", Name: "search"}},
		{Type: ToolArgumentsReceived, Tool: &ToolEvent{Index: 0, Arguments: `{invalid}`}},
		{Type: ToolCallCompleted, Tool: &ToolEvent{Index: 0}},
		{Type: StreamCompleted},
	})
	if err == nil {
		t.Fatal("expected malformed tool arguments error")
	}
}
