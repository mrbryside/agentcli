package agentcli

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"
)

func TestSubagentStepLimitFinalizerReturnsPartialTextWithoutTools(t *testing.T) {
	model := &lateStepLimitFinalTextModel{}
	subagent, err := New(
		context.Background(),
		withSubagentAgent(),
		WithModel(model),
		WithProviderStepLimit(1),
		WithTool(testTool("work")),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer subagent.Close()

	run, err := subagent.Start(context.Background(), userRequest("subagent-step-limit"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	result, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if !run.StepLimitFinalized() || run.CompletionRepairCount() != 0 {
		t.Fatalf("finalized/repairs = %v/%d, want true/0", run.StepLimitFinalized(), run.CompletionRepairCount())
	}
	if result.Content != "partial final answer" || result.Steps != 2 {
		t.Fatalf("result = %#v, want partial final answer after two provider steps", result)
	}
	task := taskResultFromFinalOutput("task-1", SubagentDefinition{Name: "researcher"}, result.Content, run.StepLimitFinalized())
	if task.State != TaskStateIncomplete || task.Output != "partial final answer" {
		t.Fatalf("task result = %#v, want incomplete partial output", task)
	}

	requests := model.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want agentic and text-only finalizer", len(requests))
	}
	if len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "work" {
		t.Fatalf("agentic request tools = %#v, want work only", requests[0].Tools)
	}
	if len(requests[1].Tools) != 0 {
		t.Fatalf("finalization request tools = %#v, want none", requests[1].Tools)
	}
}

func TestSubagentsRejectEndResponseScopeTools(t *testing.T) {
	t.Run("root project validation", func(t *testing.T) {
		root := projectFixture(t)
		writeSubagentDefinition(t, root, "researcher", `---
name: researcher
description: Research project files.
provider: openai
model: gpt-test
tools: [deliver]
---
Research and deliver.
`)
		project, err := LoadProject(root)
		if err != nil {
			t.Fatal(err)
		}
		_, err = New(
			context.Background(),
			WithProject(project),
			WithModel(&scriptedModel{}),
			WithTool(endResponseScopeTestTool("deliver")),
		)
		if err == nil ||
			!strings.Contains(err.Error(), `subagent "researcher"`) ||
			!strings.Contains(err.Error(), `custom tool "deliver"`) ||
			!strings.Contains(err.Error(), "supported only by main agents") {
			t.Fatalf("New() error = %v, want named subagent EndResponseScope rejection", err)
		}
	})

	t.Run("defensive subagent construction", func(t *testing.T) {
		_, err := New(
			context.Background(),
			withSubagentAgent(),
			WithModel(&scriptedModel{}),
			WithTool(endResponseScopeTestTool("deliver")),
		)
		if err == nil ||
			!strings.Contains(err.Error(), `custom tool "deliver"`) ||
			!strings.Contains(err.Error(), "supported only by main agents") {
			t.Fatalf("New() error = %v, want defensive subagent EndResponseScope rejection", err)
		}
	})
}

func endResponseScopeTestTool(name string) Tool {
	return Tool{
		Definition: ToolDefinition{
			Name:        name,
			Description: "Deliver a response.",
			InputSchema: ObjectSchema(struct{}{}),
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
		Trigger: EndResponseScope,
	}
}

type lateStepLimitFinalTextModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
}

func (m *lateStepLimitFinalTextModel) Start(
	_ context.Context,
	request agentruntime.ModelRequest,
) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, request)
	m.mu.Unlock()

	switch index {
	case 0:
		return scriptedStream{result: provider.StreamResult{
			CompletedTools: []provider.ToolCall{{
				ID: "work", Name: "work", Arguments: map[string]any{},
			}},
			Finished: true,
		}}, nil
	case 1:
		return scriptedStream{result: provider.StreamResult{
			Content: "partial final answer", Finished: true,
		}}, nil
	default:
		return scriptedStream{result: provider.StreamResult{Content: "unexpected", Finished: true}}, nil
	}
}

func (m *lateStepLimitFinalTextModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}
