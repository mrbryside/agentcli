package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

func TestInputGuardRejectsBeforePersisting(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	guard := func(_ context.Context, attempt InputGuardAttempt) (InputGuardDecision, error) {
		if attempt.Message.Content != "blocked" {
			t.Fatalf("guard input = %#v", attempt.Message)
		}
		return InputGuardDecision{Action: InputReject, Reason: "blocked by policy"}, nil
	}
	runtime, err := New(context.Background(), Config{
		Model: runtimeModel{}, Messages: messages,
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
		InputGuard: guard,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), Request{
		SessionID: "guard-input", TurnID: "turn-1",
		Message: Message{Type: MessageTypeUser, Content: "blocked"},
	})
	if !errors.Is(err, ErrInputGuardRejected) {
		t.Fatalf("Start() error = %v, want ErrInputGuardRejected", err)
	}
	stored, err := messages.List(context.Background(), "guard-input")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored messages = %#v, want none", stored)
	}
}

func TestInputGuardReplacementCannotChangeMessageType(t *testing.T) {
	runtime, err := New(context.Background(), Config{
		Model: runtimeModel{}, Messages: inmemory.NewMessageStorage(),
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
		InputGuard: func(_ context.Context, _ InputGuardAttempt) (InputGuardDecision, error) {
			return InputGuardDecision{Action: InputReplace, Message: &Message{Type: MessageTypeAssistant, Content: "changed role"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), Request{
		SessionID: "guard-input-type", TurnID: "turn-1",
		Message: Message{Type: MessageTypeUser, Content: "hello"},
	})
	if err == nil || !strings.Contains(err.Error(), "message type changes") {
		t.Fatalf("Start() error = %v, want changed message type rejection", err)
	}
}

func TestInputGuardPanicReturnsErrorBeforePersisting(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	runtime, err := New(context.Background(), Config{
		Model: runtimeModel{}, Messages: messages,
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
		InputGuard: func(context.Context, InputGuardAttempt) (InputGuardDecision, error) {
			panic("broken policy")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), Request{
		SessionID: "guard-input-panic", TurnID: "turn-1",
		Message: Message{Type: MessageTypeUser, Content: "hello"},
	})
	if err == nil || !strings.Contains(err.Error(), "guard panicked: broken policy") {
		t.Fatalf("Start() error = %v, want recovered guard panic", err)
	}
	stored, listErr := messages.List(context.Background(), "guard-input-panic")
	if listErr != nil || len(stored) != 0 {
		t.Fatalf("stored messages = %#v, error = %v", stored, listErr)
	}
}

func TestOutputGuardRetriesWithFeedback(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{
			Type:    provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "unsafe answer", Finished: true}},
		}}},
		scriptedStream{events: []provider.StreamEvent{{
			Type:    provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "safe answer", Finished: true}},
		}}},
	}}
	var attempts []OutputGuardAttempt
	guard := func(_ context.Context, attempt OutputGuardAttempt) (OutputGuardDecision, error) {
		attempts = append(attempts, cloneOutputGuardAttempt(attempt))
		if len(attempts) == 1 {
			return OutputGuardDecision{Action: OutputRetry, Feedback: "remove unsafe content"}, nil
		}
		return OutputGuardDecision{Action: OutputProceed}, nil
	}
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: inmemory.NewMessageStorage(),
		ToolRequests: make(chan ToolRequest, 2), ToolResults: make(chan ToolResultEnvelope, 2), ToolInterrupts: make(chan ToolInterrupt, 2),
		OutputGuard: guard, MaxSteps: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "guard-output", TurnID: "turn-1",
		Message: Message{Type: MessageTypeUser, Content: "answer me"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRuntimeEvents(t, run)
	result, err := run.Result()
	if err != nil || result.Content != "safe answer" {
		t.Fatalf("result = (%#v, %v)", result, err)
	}
	if run.OutputGuardRetryCount() != 1 {
		t.Fatalf("output guard retries = %d, want 1", run.OutputGuardRetryCount())
	}
	requests := model.Requests()
	if len(requests) != 2 || len(requests[1].ContextReminders) != 1 || requests[1].ContextReminders[0].Content != "remove unsafe content" {
		t.Fatalf("provider requests = %#v", requests)
	}
	if len(attempts) != 2 || attempts[0].Output.Content != "unsafe answer" || attempts[1].Output.Content != "safe answer" {
		t.Fatalf("guard attempts = %#v", attempts)
	}
}

func TestOutputGuardPanicFailsRun(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{
			Type: provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
				Content: "candidate", Finished: true,
			}},
		}}},
	}}
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: inmemory.NewMessageStorage(),
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
		OutputGuard: func(context.Context, OutputGuardAttempt) (OutputGuardDecision, error) {
			panic("broken policy")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "guard-output-panic", TurnID: "turn-1",
		Message: Message{Type: MessageTypeUser, Content: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRuntimeEvents(t, run)
	if _, err := run.Result(); err == nil || !strings.Contains(err.Error(), "guard panicked: broken policy") {
		t.Fatalf("Result() error = %v, want recovered guard panic", err)
	}
}

func TestPromptInputGuardUsesDefaultModel(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{
			Type:    provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: `{"allowed":true,"reason":"","feedback":""}`, Finished: true}},
		}}},
		scriptedStream{events: []provider.StreamEvent{{
			Type:    provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}},
		}}},
	}}
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: inmemory.NewMessageStorage(),
		ToolRequests: make(chan ToolRequest, 2), ToolResults: make(chan ToolResultEnvelope, 2), ToolInterrupts: make(chan ToolInterrupt, 2),
		InputGuardPrompt: "allow ordinary user input",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "prompt-input", TurnID: "turn-1",
		Message: Message{Type: MessageTypeUser, Content: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRuntimeEvents(t, run)
	if result, err := run.Result(); err != nil || result.Content != "done" {
		t.Fatalf("result = (%#v, %v)", result, err)
	}
	requests := model.Requests()
	if len(requests) != 2 || !strings.Contains(requests[0].SystemPrompts[0], "allow ordinary user input") {
		t.Fatalf("guard model requests = %#v", requests)
	}
	if len(requests[0].Messages) != 2 || !strings.Contains(requests[0].Messages[0].Content, `"Content":"hello"`) {
		t.Fatalf("guard candidate messages = %#v", requests[0].Messages)
	}
	for _, required := range []string{"<guard_response_rules>", "Return the required JSON object immediately.", "reasoning is taking too long"} {
		if !strings.Contains(requests[0].Messages[1].Content, required) {
			t.Fatalf("guard response rules %q do not contain %q", requests[0].Messages[1].Content, required)
		}
	}
}

func TestPromptOutputGuardRetriesWithModelFeedback(t *testing.T) {
	model := &scriptedRuntimeModel{streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{
			Type:    provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "bad", Finished: true}},
		}}},
		scriptedStream{events: []provider.StreamEvent{{
			Type:    provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: `{"allowed":false,"reason":"policy violation","feedback":"answer safely"}`, Finished: true}},
		}}},
		scriptedStream{events: []provider.StreamEvent{{
			Type:    provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "good", Finished: true}},
		}}},
		scriptedStream{events: []provider.StreamEvent{{
			Type:    provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: `{"allowed":true,"reason":"","feedback":""}`, Finished: true}},
		}}},
	}}
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: inmemory.NewMessageStorage(),
		ToolRequests: make(chan ToolRequest, 4), ToolResults: make(chan ToolResultEnvelope, 4), ToolInterrupts: make(chan ToolInterrupt, 4),
		OutputGuardPrompt: "only allow safe answers",
		MaxSteps:          3,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "prompt-output", TurnID: "turn-1",
		Message: Message{Type: MessageTypeUser, Content: "answer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRuntimeEvents(t, run)
	result, err := run.Result()
	if err != nil || result.Content != "good" {
		t.Fatalf("result = (%#v, %v)", result, err)
	}
	requests := model.Requests()
	if len(requests) != 4 || len(requests[2].ContextReminders) != 1 || requests[2].ContextReminders[0].Content != "answer safely" {
		t.Fatalf("provider requests = %#v", requests)
	}
}

func cloneOutputGuardAttempt(attempt OutputGuardAttempt) OutputGuardAttempt {
	attempt.Messages = storage.CloneMessages(attempt.Messages)
	attempt.Output = storage.CloneMessage(attempt.Output)
	return attempt
}

func TestDecodePromptGuardVerdictIsStrict(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "valid", content: `{"allowed":false,"reason":"unsafe","feedback":"retry safely"}`},
		{name: "fenced", content: "```json\n{\"allowed\":true,\"reason\":\"\",\"feedback\":\"\"}\n```"},
		{name: "missing allowed", content: `{"reason":"unsafe","feedback":"retry"}`, wantErr: true},
		{name: "missing reason", content: `{"allowed":false,"feedback":"retry"}`, wantErr: true},
		{name: "missing feedback", content: `{"allowed":false,"reason":"unsafe"}`, wantErr: true},
		{name: "unknown field", content: `{"allowed":true,"reason":"","feedback":"","extra":true}`, wantErr: true},
		{name: "allowed with feedback", content: `{"allowed":true,"reason":"","feedback":"retry"}`, wantErr: true},
		{name: "leading prose", content: `verdict: {"allowed":true,"reason":"","feedback":""}`, wantErr: true},
		{name: "trailing prose", content: `{"allowed":true,"reason":"","feedback":""} approved`, wantErr: true},
		{name: "multiple values", content: `{"allowed":true,"reason":"","feedback":""} {"allowed":true,"reason":"","feedback":""}`, wantErr: true},
		{name: "unterminated fence", content: "```json\n{\"allowed\":true,\"reason\":\"\",\"feedback\":\"\"}", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var verdict promptGuardVerdict
			err := decodePromptGuardVerdict(test.content, &verdict)
			if test.wantErr && err == nil {
				t.Fatal("decode error = nil, want rejection")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("decode error = %v", err)
			}
		})
	}
}
