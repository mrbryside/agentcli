package agentcli

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

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
	agent, err := New(context.Background(), WithModel(model), WithTool(tool))
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
	if requests[0].ToolChoice == nil || requests[0].ToolChoice.Mode != agentruntime.ToolChoiceRequired {
		t.Fatalf("initial request tool choice = %#v, want required", requests[0].ToolChoice)
	}
	if len(requests[1].Tools) != 1 || requests[1].Tools[0].Name != "report" {
		t.Fatalf("repair tools = %#v", requests[1].Tools)
	}
	if requests[1].ToolChoice == nil || requests[1].ToolChoice.Mode != agentruntime.ToolChoiceSpecific || requests[1].ToolChoice.Name != "report" {
		t.Fatalf("repair tool choice = %#v", requests[1].ToolChoice)
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
	if requests[1].ToolChoice == nil || requests[1].ToolChoice.Mode != agentruntime.ToolChoiceRequired {
		t.Fatalf("follow-up choice = %#v, want required", requests[1].ToolChoice)
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
