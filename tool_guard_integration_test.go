package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"
)

func TestPromptToolOutputGuardFeedsFailureBackAndAgentRetries(t *testing.T) {
	model := &toolGuardLoopModel{}
	agent, err := New(context.Background(),
		WithModel(model),
		WithTool(Tool{
			Definition: ToolDefinition{Name: "lookup", InputSchema: InputSchema{Type: "object"}},
			Handler: func(_ context.Context, arguments json.RawMessage) (json.RawMessage, error) {
				return append(json.RawMessage(nil), arguments...), nil
			},
			TurnBehavior:          EndTurn,
			ToolOutputGuardPrompt: "Require attempt 2 or later.",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("tool-output-guard-loop"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	result, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolResults) != 2 || result.ToolResults[0].Status != agentruntime.ToolResultFailed || result.ToolResults[1].Status != agentruntime.ToolResultSucceeded {
		t.Fatalf("tool results = %#v", result.ToolResults)
	}
	if !strings.Contains(result.ToolResults[0].Error, "attempt 2") || string(result.ToolResults[1].Output) != `{"attempt":2}` {
		t.Fatalf("tool results = %#v", result.ToolResults)
	}

	agentRequests, guardRequests := model.Requests()
	if len(agentRequests) != 2 || len(guardRequests) != 2 {
		t.Fatalf("model requests = agent:%d guard:%d", len(agentRequests), len(guardRequests))
	}
	last := agentRequests[1].Messages[len(agentRequests[1].Messages)-1]
	if last.Type != agentruntime.MessageTypeToolResult || last.ToolResult == nil || last.ToolResult.Status != agentruntime.ToolResultFailed || !strings.Contains(last.ToolResult.Error, "attempt 2") {
		t.Fatalf("second agent request transcript = %#v", agentRequests[1].Messages)
	}
}

type toolGuardLoopModel struct {
	mu            sync.Mutex
	agentRequests []agentruntime.ModelRequest
	guardRequests []agentruntime.ModelRequest
}

func (model *toolGuardLoopModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(request.SystemPrompts) != 0 && strings.Contains(request.SystemPrompts[0], "tool output guard") {
		model.guardRequests = append(model.guardRequests, request)
		content := `{"allowed":true,"reason":"valid","feedback":""}`
		if len(model.guardRequests) == 1 {
			content = `{"allowed":false,"reason":"stale","feedback":"call lookup again with attempt 2"}`
		}
		return toolGuardLoopStream{result: provider.StreamResult{Content: content, Finished: true}}, nil
	}
	model.agentRequests = append(model.agentRequests, request)
	attempt := len(model.agentRequests)
	return toolGuardLoopStream{result: provider.StreamResult{
		CompletedTools: []provider.ToolCall{{ID: "call-" + string(rune('0'+attempt)), Name: "lookup", Arguments: map[string]any{"attempt": attempt}}},
		Finished:       true,
	}}, nil
}

func (model *toolGuardLoopModel) Requests() ([]agentruntime.ModelRequest, []agentruntime.ModelRequest) {
	model.mu.Lock()
	defer model.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), model.agentRequests...), append([]agentruntime.ModelRequest(nil), model.guardRequests...)
}

type toolGuardLoopStream struct{ result provider.StreamResult }

func (stream toolGuardLoopStream) Subscribe(context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	events <- provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: stream.result}}
	close(events)
	return events
}

func (toolGuardLoopStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, errors.New("unused")
}
