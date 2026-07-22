package agentruntime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

func TestCompletionGuardRetriesWithPersistedMessagesAndRestrictedTools(t *testing.T) {
	model := &completionGuardModel{contents: []string{"unstructured child answer", "repaired child answer"}}
	requests := make(chan ToolRequest, 4)
	results := make(chan ToolResultEnvelope, 4)
	interrupts := make(chan ToolInterrupt, 4)
	var attempts []CompletionAttempt
	guard := func(_ context.Context, attempt CompletionAttempt) (CompletionDecision, error) {
		attempts = append(attempts, cloneCompletionAttempt(attempt))
		if attempt.RepairCount == 0 {
			return CompletionDecision{
				Action:           CompletionRetry,
				ContextReminders: []ContextReminder{{Content: "report the semantic outcome only"}},
				ToolAllowlist:    []string{"report_outcome"},
			}, nil
		}
		return CompletionDecision{Action: CompletionProceed}, nil
	}
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: inmemory.NewMessageStorage(),
		Tools: []ToolDefinition{
			{Name: "domain_action", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "report_outcome", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts,
		CompletionGuard: guard, MaxSteps: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "completion-session", TurnID: "completion-turn",
		Message: Message{Type: MessageTypeUser, Content: "complete the child task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectRuntimeEvents(t, run)
	if result, err := run.Result(); err != nil || result.Content != "repaired child answer" || result.Steps != 2 {
		t.Fatalf("result = (%#v, %v)", result, err)
	}
	if run.CompletionRepairCount() != 1 {
		t.Fatalf("repair count = %d, want 1", run.CompletionRepairCount())
	}
	if len(events) == 0 || events[len(events)-1].Type != RunCompleted {
		t.Fatalf("terminal events = %#v", events)
	}

	providerRequests := model.Requests()
	if len(providerRequests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(providerRequests))
	}
	if len(providerRequests[0].Tools) != 2 {
		t.Fatalf("initial tools = %#v", providerRequests[0].Tools)
	}
	repair := providerRequests[1]
	if len(repair.Tools) != 1 || repair.Tools[0].Name != "report_outcome" {
		t.Fatalf("repair tools = %#v", repair.Tools)
	}
	if len(repair.ContextReminders) != 1 || repair.ContextReminders[0].Content != "report the semantic outcome only" {
		t.Fatalf("repair reminders = %#v", repair.ContextReminders)
	}
	if len(repair.Messages) != 2 || repair.Messages[1].Type != MessageTypeAssistant || repair.Messages[1].Content != "unstructured child answer" {
		t.Fatalf("repair transcript = %#v", repair.Messages)
	}
	if len(attempts) != 2 || attempts[0].RepairCount != 0 || attempts[1].RepairCount != 1 {
		t.Fatalf("completion attempts = %#v", attempts)
	}
	if len(attempts[0].Messages) != 2 || attempts[0].Messages[1].Content != "unstructured child answer" {
		t.Fatalf("first completion snapshot = %#v", attempts[0].Messages)
	}
}

type completionGuardModel struct {
	mu       sync.Mutex
	requests []ModelRequest
	contents []string
}

func (m *completionGuardModel) Start(_ context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, request)
	content := m.contents[index]
	m.mu.Unlock()
	return scriptedStream{events: []provider.StreamEvent{{
		Type: provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
			Content: content, Finished: true,
		}},
	}}}, nil
}

func (m *completionGuardModel) Requests() []ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ModelRequest(nil), m.requests...)
}
