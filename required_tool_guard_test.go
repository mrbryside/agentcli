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

type requiredFinalizerModel struct {
	mu           sync.Mutex
	requests     []agentruntime.ModelRequest
	repairMisses int
	starts       int
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
