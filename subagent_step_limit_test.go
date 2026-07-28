package agentcli

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestSubagentStepLimitFinalizerRepairsOutcomeAndLeavesFinalTextRound(t *testing.T) {
	model := &lateStepLimitOutcomeModel{}
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
	if !run.StepLimitFinalized() || run.CompletionRepairCount() != defaultCompletionRepairLimit {
		t.Fatalf(
			"finalized/repairs = %v/%d, want true/%d",
			run.StepLimitFinalized(),
			run.CompletionRepairCount(),
			defaultCompletionRepairLimit,
		)
	}
	if result.Content != "final subagent answer" || result.Steps != 6 {
		t.Fatalf("result = %#v, want final subagent answer after six provider steps", result)
	}

	requests := model.Requests()
	if len(requests) != 6 {
		t.Fatalf("provider requests = %d, want agentic, finalizer, three repairs, and final text", len(requests))
	}
	for index := 1; index < len(requests); index++ {
		if len(requests[index].Tools) != 1 ||
			requests[index].Tools[0].Name != toolexecution.SubagentResultToolName {
			t.Fatalf(
				"finalization request %d tools = %#v, want only report_subagent_result",
				index,
				requests[index].Tools,
			)
		}
	}
	if hasSubagentReportRepairReminder(requests[1]) {
		t.Fatal("initial finalizer must not be counted as an outcome repair")
	}
	for index := 2; index <= 4; index++ {
		if !hasSubagentReportRepairReminder(requests[index]) {
			t.Fatalf("request %d lacks the outcome repair reminder", index)
		}
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

type lateStepLimitOutcomeModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
}

func (m *lateStepLimitOutcomeModel) Start(
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
	case 4:
		return scriptedStream{result: provider.StreamResult{
			CompletedTools: []provider.ToolCall{{
				ID:   "outcome",
				Name: toolexecution.SubagentResultToolName,
				Arguments: map[string]any{
					"status":  string(toolexecution.SubagentReportCompleted),
					"summary": "completed from existing results",
				},
			}},
			Finished: true,
		}}, nil
	case 5:
		return scriptedStream{result: provider.StreamResult{
			Content: "final subagent answer", Finished: true,
		}}, nil
	default:
		return scriptedStream{result: provider.StreamResult{
			Content: "forgot the outcome report", Finished: true,
		}}, nil
	}
}

func (m *lateStepLimitOutcomeModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}
