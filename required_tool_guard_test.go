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

func TestRequiredRawToolRepairsOneMissingTriggerToolCall(t *testing.T) {
	model := &requiredTriggerToolModel{}
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
		Trigger: EndTurn,
	}
	distractor := Tool{
		Definition: ToolDefinition{
			Name:        "distractor",
			Description: "A normal tool that must not be exposed during strict trigger tool repair.",
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
	run, err := agent.Start(context.Background(), userRequest("required-trigger-tool"))
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

func TestEndResponseScopeAutomaticallyRequiresAndDefersTriggerTool(t *testing.T) {
	model := &requiredTriggerToolModel{}
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
		Trigger: EndResponseScope,
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
	model := &requiredTriggerToolModel{repairMisses: 2}
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
		Trigger: EndTurn,
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("required-trigger-tool-bounded"))
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

func TestRequiredTriggerToolMixedContinuingBatchRequiresItAgain(t *testing.T) {
	model := &requiredMixedBatchModel{}
	report := Tool{
		Definition: ToolDefinition{Name: "report", Description: "Required report.", InputSchema: ObjectSchema(struct{ Message ToolParameter }{Message: StringParameter("message").Required()})},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
		Trigger: EndTurn,
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
	run, err := agent.Start(context.Background(), userRequest("mixed-trigger-tool"))
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

func TestEndTurnOnSuccessIsOptionalAndDoesNotRunWhenOmitted(t *testing.T) {
	model := &scriptedModel{}
	var calls int
	tool := Tool{
		Definition:       ToolDefinition{Name: "handoff", Description: "Optional terminal handoff.", InputSchema: ObjectSchema(struct{}{})},
		EndTurnOnSuccess: true,
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("optional-end-turn-on-success"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || run.CompletionRepairCount() != 0 || len(model.Requests()) != 1 {
		t.Fatalf("calls = %d, repairs = %d, requests = %d; want 0, 0, 1", calls, run.CompletionRepairCount(), len(model.Requests()))
	}
}

func TestEndTurnOnSuccessEndsSuccessfulMixedBatch(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{
		{ID: "handoff", Name: "handoff", Arguments: map[string]any{}},
		{ID: "audit", Name: "audit", Arguments: map[string]any{}},
	}}
	var handoffs, audits int
	handoff := Tool{
		Definition:       ToolDefinition{Name: "handoff", Description: "Optional terminal handoff.", InputSchema: ObjectSchema(struct{}{})},
		EndTurnOnSuccess: true,
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			handoffs++
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	audit := Tool{
		Definition: ToolDefinition{Name: "audit", Description: "Normal immediate tool.", InputSchema: ObjectSchema(struct{}{})},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			audits++
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(handoff), WithTool(audit))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("mixed-end-turn-on-success"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	if handoffs != 1 || audits != 1 || len(model.Requests()) != 1 {
		t.Fatalf("handoffs = %d, audits = %d, requests = %d; want 1, 1, 1", handoffs, audits, len(model.Requests()))
	}
}

func TestEndTurnOnSuccessCannotBypassRequiredTrigger(t *testing.T) {
	model := &endTurnOnSuccessRepairModel{}
	var handoffs, reports int
	handoff := Tool{
		Definition:       ToolDefinition{Name: "handoff", Description: "Optional terminal handoff.", InputSchema: ObjectSchema(struct{}{})},
		EndTurnOnSuccess: true,
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			handoffs++
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	report := Tool{
		Definition: ToolDefinition{Name: "report", Description: "Required final report.", InputSchema: ObjectSchema(struct{}{})},
		Trigger:    EndTurn,
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reports++
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(handoff), WithTool(report))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("end-turn-on-success-required-repair"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if handoffs != 1 || reports != 1 || run.CompletionRepairCount() != 1 || len(requests) != 2 {
		t.Fatalf("handoffs = %d, reports = %d, repairs = %d, requests = %d; want 1, 1, 1, 2", handoffs, reports, run.CompletionRepairCount(), len(requests))
	}
	if len(requests[1].Tools) != 1 || requests[1].Tools[0].Name != "report" {
		t.Fatalf("repair tools = %#v, want only report", requests[1].Tools)
	}
}

func TestRequiredTriggerToolRepairMergesBaseBoundedToolAllowlist(t *testing.T) {
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

type requiredTriggerToolModel struct {
	mu           sync.Mutex
	requests     []agentruntime.ModelRequest
	repairMisses int
	starts       int
}

type requiredMixedBatchModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
}

type endTurnOnSuccessRepairModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
}

func (m *endTurnOnSuccessRepairModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	if index == 0 {
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "handoff", Name: "handoff", Arguments: map[string]any{}}}, Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "report", Name: "report", Arguments: map[string]any{}}}, Finished: true}}, nil
}

func (m *endTurnOnSuccessRepairModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
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

func (m *requiredTriggerToolModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	m.starts++
	if m.starts > 1+m.repairMisses {
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "report-repair", Name: "report", Arguments: map[string]any{"message": "done"}}}, Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{Content: "done", Finished: true}}, nil
}

func (m *requiredTriggerToolModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}
