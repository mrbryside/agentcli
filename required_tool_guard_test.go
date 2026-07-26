package agentcli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"
)

func TestRequiredRawToolRepairsOneMissingFinalizerCall(t *testing.T) {
	model := &requiredFinalizerModel{}
	var calls int
	tool := Tool{
		Definition: ToolDefinition{
			Name:        "report",
			Description: "Required final report.",
			InputSchema: ObjectSchema(struct{ Message ToolParameter }{Message: StringParameter("Final message").Required()}),
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"ok":true}`), nil
		},
		TurnBehavior:      EndTurn,
		RequiredAtTurnEnd: true,
	}
	distractor := Tool{
		Definition: ToolDefinition{
			Name:        "distractor",
			Description: "A normal tool that must not be exposed during strict finalizer repair.",
			InputSchema: ObjectSchema(struct{}{}),
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(tool), WithTool(distractor))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("required-finalizer"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || run.CompletionRepairCount() != 1 {
		t.Fatalf("calls = %d, repair count = %d", calls, run.CompletionRepairCount())
	}
	requests := model.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want initial plus one repair", len(requests))
	}
	if len(requests[1].Tools) != 1 || requests[1].Tools[0].Name != "report" {
		t.Fatalf("repair tools = %#v", requests[1].Tools)
	}
	if len(requests[1].ContextReminders) != 1 || !strings.Contains(requests[1].ContextReminders[0].Content, "report") {
		t.Fatalf("repair reminder = %#v", requests[1].ContextReminders)
	}
}

func TestAfterResponseScopeAutomaticallyRequiresAndDefersFinalizer(t *testing.T) {
	model := &requiredFinalizerModel{}
	delivered := make(chan struct{}, 1)
	tool := Tool{
		Definition: ToolDefinition{
			Name:        "report",
			Description: "Response-scope report.",
			InputSchema: ObjectSchema(struct{ Message ToolParameter }{Message: StringParameter("Final message").Required()}),
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			delivered <- struct{}{}
			return json.RawMessage(`{"ok":true}`), nil
		},
		Lifecycle: AfterResponseScope,
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("after-response-scope"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if run.CompletionRepairCount() != 1 {
		t.Fatalf("completion repairs = %d, want automatic required-tool repair", run.CompletionRepairCount())
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("after-response-scope handler did not run")
	}
	messages, err := agent.ListMessages(context.Background(), run.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	var deferred bool
	for _, message := range messages {
		if message.ToolResult == nil || message.ToolResult.Name != "report" {
			continue
		}
		deferred = strings.Contains(string(message.ToolResult.Output), `"status":"deferred"`)
	}
	if !deferred {
		t.Fatalf("report tool result did not clearly report deferral: %#v", messages)
	}
}

func TestRequiredRawToolRepairsUntilBoundedSuccess(t *testing.T) {
	model := &requiredFinalizerModel{repairMisses: 2}
	var calls int
	tool := Tool{
		Definition: ToolDefinition{
			Name:        "report",
			Description: "Required final report.",
			InputSchema: ObjectSchema(struct{ Message ToolParameter }{Message: StringParameter("Final message").Required()}),
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"ok":true}`), nil
		},
		TurnBehavior:      EndTurn,
		RequiredAtTurnEnd: true,
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("required-finalizer-bounded"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || run.CompletionRepairCount() != defaultCompletionRepairLimit {
		t.Fatalf("calls = %d, repair count = %d", calls, run.CompletionRepairCount())
	}
	if requests := model.Requests(); len(requests) != defaultCompletionRepairLimit+1 {
		t.Fatalf("provider requests = %d, want initial plus bounded repairs", len(requests))
	}
}

func TestRequiredFinalizerMixedContinuingBatchRequiresItAgain(t *testing.T) {
	model := &requiredMixedBatchModel{}
	report := Tool{
		Definition: ToolDefinition{Name: "report", Description: "Required report.", InputSchema: ObjectSchema(struct{ Message ToolParameter }{Message: StringParameter("message").Required()})},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
		TurnBehavior: EndTurn, RequiredAtTurnEnd: true,
	}
	work := Tool{
		Definition: ToolDefinition{Name: "work", Description: "Continue work.", InputSchema: ObjectSchema(struct{}{})},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(report), WithTool(work))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("mixed-finalizer"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
}

func TestRequiredFinalizerRepairMergesBaseBoundedToolAllowlist(t *testing.T) {
	base := func(context.Context, agentruntime.CompletionAttempt) (agentruntime.CompletionDecision, error) {
		return agentruntime.CompletionDecision{
			Action:           agentruntime.CompletionRetry,
			ContextReminders: []agentruntime.ContextReminder{{Content: "revise the draft"}},
			ToolAllowlist:    []string{"revise", "report"},
		}, nil
	}
	guard := completionGuardWithRequiredTools(base, []string{"report"})
	decision, err := guard(context.Background(), agentruntime.CompletionAttempt{
		SessionID: "session",
		TurnID:    "turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != agentruntime.CompletionRetry || !slices.Equal(decision.ToolAllowlist, []string{"report", "revise"}) {
		t.Fatalf("decision = %#v", decision)
	}
	if len(decision.ContextReminders) != 2 {
		t.Fatalf("reminders = %#v", decision.ContextReminders)
	}
}

type requiredFinalizerModel struct {
	mu           sync.Mutex
	requests     []agentruntime.ModelRequest
	repairMisses int
	starts       int
}

type requiredMixedBatchModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
}

func (m *requiredMixedBatchModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	if index == 0 {
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "report-first", Name: "report", Arguments: map[string]any{"message": "early"}}, {ID: "work", Name: "work", Arguments: map[string]any{}}}, Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "report-final", Name: "report", Arguments: map[string]any{"message": "final"}}}, Finished: true}}, nil
}

func (m *requiredMixedBatchModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}

func (m *requiredFinalizerModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	m.starts++
	if m.starts > 1+m.repairMisses {
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "report-repair", Name: "report", Arguments: map[string]any{"message": "done"}}}, Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{Content: "done", Finished: true}}, nil
}

func (m *requiredFinalizerModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}
