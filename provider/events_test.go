package provider

import "testing"

func TestEventTypesArePastTenseStrings(t *testing.T) {
	tests := map[EventType]string{
		ContentReceived:       "content_received",
		ReasoningReceived:     "reasoning_received",
		ToolCallStarted:       "tool_call_started",
		ToolArgumentsReceived: "tool_arguments_received",
		ToolCallCompleted:     "tool_call_completed",
		StreamCompleted:       "stream_completed",
		StreamFailed:          "stream_failed",
	}

	for eventType, want := range tests {
		if string(eventType) != want {
			t.Fatalf("event type %q = %q, want %q", eventType, eventType, want)
		}
	}
}

func TestStreamEventCarriesTypedToolAndTerminalPayloads(t *testing.T) {
	tool := ToolEvent{Index: 1, ID: "call_1", Type: "function", Name: "search"}
	completed := StreamCompletedPayload{Result: StreamResult{Finished: true}}
	failed := StreamFailedPayload{Error: errTest}

	event := StreamEvent{
		Type:    ToolCallStarted,
		Tool:    &tool,
		Payload: completed,
	}
	if event.Tool == nil || event.Tool.ID != "call_1" {
		t.Fatalf("tool payload was not retained: %#v", event.Tool)
	}
	if event.Payload.(StreamCompletedPayload).Result.Finished != true {
		t.Fatal("completion payload was not retained")
	}

	failureEvent := StreamEvent{Type: StreamFailed, Payload: failed}
	if failureEvent.Payload.(StreamFailedPayload).Error != errTest {
		t.Fatal("failure payload was not retained")
	}
}

var errTest = testError("test error")

type testError string

func (e testError) Error() string { return string(e) }
