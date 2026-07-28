package agentcli

import (
	"context"
	"encoding/json"
	"errors"
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
	if len(requests) != 3 {
		t.Fatalf("provider requests = %d, want initial response, repair tool call, and final response", len(requests))
	}
	if len(requests[1].Tools) != 1 || requests[1].Tools[0].Name != "report" {
		t.Fatalf("repair tools = %#v", requests[1].Tools)
	}
	if len(requests[1].ContextReminders) != 1 || !strings.Contains(requests[1].ContextReminders[0].Content, "report") {
		t.Fatalf("repair reminder = %#v", requests[1].ContextReminders)
	}
}

func TestEndResponseScopeAutomaticallyRequiresAndExecutesTriggerAtBoundary(t *testing.T) {
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
	if requests := model.Requests(); len(requests) != 3 {
		t.Fatalf("provider requests = %d, want trigger call followed by final response without implicit turn end", len(requests))
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
	var executed bool
	for _, message := range messages {
		if message.ToolResult == nil || message.ToolResult.Name != "report" {
			continue
		}
		executed = message.ToolResult.TriggerSatisfied != nil &&
			*message.ToolResult.TriggerSatisfied
	}
	if !executed {
		t.Fatalf("report tool result did not satisfy the final trigger: %#v", messages)
	}
}

func TestStepLimitFinalizationRepairsForgottenEndResponseScopeTool(t *testing.T) {
	model := &stepLimitFinalizationModel{reportAt: 2}
	var delivered int
	report := Tool{
		Definition: ToolDefinition{
			Name:        "report",
			Description: "Response-scope report.",
			InputSchema: ObjectSchema(struct{ Message ToolParameter }{Message: StringParameter("Final message").Required()}),
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			delivered++
			return json.RawMessage(`{"ok":true}`), nil
		},
		Trigger:          EndResponseScope,
		EndTurnOnSuccess: true,
	}
	agent, err := New(
		context.Background(),
		WithModel(model),
		WithProviderStepLimit(1),
		WithTool(testTool("work")),
		WithTool(report),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("step-limit-finalization-repair"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	result, err := run.Result()
	if err != nil || !run.StepLimitFinalized() || run.CompletionRepairCount() != 1 || delivered != 1 {
		t.Fatalf("result = (%#v, %v), finalized=%v repairs=%d delivered=%d", result, err, run.StepLimitFinalized(), run.CompletionRepairCount(), delivered)
	}
	requests := model.Requests()
	if len(requests) != 3 || result.Steps != 3 {
		t.Fatalf("provider requests/steps = %d/%d, want 3/3", len(requests), result.Steps)
	}
	for index := 1; index < len(requests); index++ {
		if len(requests[index].Tools) != 1 || requests[index].Tools[0].Name != "report" {
			t.Fatalf("finalization request %d tools = %#v, want only report", index, requests[index].Tools)
		}
	}
	if len(requests[2].ContextReminders) < 2 ||
		!strings.Contains(requests[2].ContextReminders[len(requests[2].ContextReminders)-1].Content, "report") {
		t.Fatalf("repair reminders = %#v, want finalization and required report reminders", requests[2].ContextReminders)
	}
}

func TestStepLimitFinalizationFailsAfterBoundedMissingEndResponseScopeRepairs(t *testing.T) {
	model := &stepLimitFinalizationModel{reportAt: -1}
	report := Tool{
		Definition: ToolDefinition{
			Name:        "report",
			Description: "Response-scope report.",
			InputSchema: ObjectSchema(struct{ Message ToolParameter }{Message: StringParameter("Final message").Required()}),
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
		Trigger:          EndResponseScope,
		EndTurnOnSuccess: true,
	}
	agent, err := New(
		context.Background(),
		WithModel(model),
		WithProviderStepLimit(1),
		WithTool(testTool("work")),
		WithTool(report),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("step-limit-finalization-exhausted"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err == nil || !strings.Contains(err.Error(), "required end-of-turn tool was not called successfully") {
		t.Fatalf("run error = %v, want bounded missing-trigger failure", err)
	}
	if got := len(model.Requests()); got != 5 {
		t.Fatalf("provider requests = %d, want one agentic plus four bounded finalization rounds", got)
	}
}

func TestEndResponseScopeFirstToolCallIsSkippedThenWorkContinues(t *testing.T) {
	model := &earlyReportThenWorkModel{}
	var reportCalls, workCalls int
	report := Tool{
		Definition: ToolDefinition{
			Name: "report",
			InputSchema: ObjectSchema(struct{ Message ToolParameter }{
				Message: StringParameter("Final message").Required(),
			}),
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reportCalls++
			return json.RawMessage(`{"status":"reported"}`), nil
		},
		Trigger:          EndResponseScope,
		EndTurnOnSuccess: true,
	}
	work := Tool{
		Definition: ToolDefinition{Name: "work", InputSchema: ObjectSchema(struct{}{})},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			workCalls++
			return json.RawMessage(`{"status":"done"}`), nil
		},
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(report), WithTool(work))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("early-report-then-work"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	if reportCalls != 1 || workCalls != 1 {
		t.Fatalf("handler calls report=%d work=%d; want report=1 work=1", reportCalls, workCalls)
	}
	messages, err := agent.ListMessages(context.Background(), run.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	var satisfactions []bool
	for _, message := range messages {
		if message.ToolResult == nil || message.ToolResult.Name != "report" {
			continue
		}
		if message.ToolResult.TriggerSatisfied == nil {
			t.Fatalf("report result has no trigger satisfaction: %#v", message.ToolResult)
		}
		satisfactions = append(satisfactions, *message.ToolResult.TriggerSatisfied)
	}
	if !slices.Equal(satisfactions, []bool{false, true}) {
		t.Fatalf("report trigger satisfactions = %v, want [false true]", satisfactions)
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
	if requests := model.Requests(); len(requests) != defaultCompletionRepairLimit+2 {
		t.Fatalf("provider requests = %d, want bounded repairs, one tool call, and one final response", len(requests))
	}
}

func TestRequiredTriggerToolMixedContinuingBatchSatisfiesRequirement(t *testing.T) {
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
	if run.CompletionRepairCount() != 0 {
		t.Fatalf("completion repairs = %d, want zero after successful trigger call", run.CompletionRepairCount())
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

func TestHandlerCanConditionallyEndSuccessfulMixedBatch(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{
		{ID: "handoff", Name: "handoff", Arguments: map[string]any{}},
		{ID: "audit", Name: "audit", Arguments: map[string]any{}},
	}}
	var handoffs, audits int
	handoff := Tool{
		Definition: ToolDefinition{Name: "handoff", Description: "Conditionally terminal handoff.", InputSchema: ObjectSchema(struct{}{})},
		Handler: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			handoffs++
			if err := RequestEndTurn(ctx); err != nil {
				return nil, err
			}
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
	run, err := agent.Start(context.Background(), userRequest("handler-requested-end-turn"))
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

func TestHandlerTurnEndRequestIsIgnoredWhenHandlerFails(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "handoff", Name: "handoff", Arguments: map[string]any{},
	}}}
	tool := Tool{
		Definition: ToolDefinition{Name: "handoff", Description: "Failing conditional handoff.", InputSchema: ObjectSchema(struct{}{})},
		Handler: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			if err := RequestEndTurn(ctx); err != nil {
				return nil, err
			}
			return nil, errors.New("handoff failed")
		},
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("failed-handler-end-turn"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	if len(model.Requests()) != 2 {
		t.Fatalf("provider requests = %d, want failed tool result followed by recovery round", len(model.Requests()))
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
		Definition:       ToolDefinition{Name: "report", Description: "Required final report.", InputSchema: ObjectSchema(struct{}{})},
		Trigger:          EndTurn,
		EndTurnOnSuccess: true,
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

func TestRequiredTriggerWithEndTurnOnSuccessEndsRepairBatch(t *testing.T) {
	model := &requiredTriggerToolModel{}
	tool := Tool{
		Definition:       ToolDefinition{Name: "report", Description: "Required final report.", InputSchema: ObjectSchema(struct{ Message ToolParameter }{Message: StringParameter("Final message").Required()})},
		Trigger:          EndTurn,
		EndTurnOnSuccess: true,
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("required-trigger-end-on-success"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	if requests := model.Requests(); len(requests) != 2 {
		t.Fatalf("provider requests = %d, want initial response and terminal repair tool call", len(requests))
	}
}

func TestMissingRequiredToolsUsesLatestAttemptInTurn(t *testing.T) {
	messages := []agentruntime.Message{
		{TurnID: "turn", Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{Name: "report", Status: agentruntime.ToolResultSucceeded}},
		{TurnID: "other-turn", Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{Name: "audit", Status: agentruntime.ToolResultSucceeded}},
		{TurnID: "turn", Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{Name: "report", Status: agentruntime.ToolResultFailed}},
		{TurnID: "turn", Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{Name: "audit", Status: agentruntime.ToolResultSucceeded}},
	}
	if got := missingRequiredTools("turn", messages, []string{"report", "audit"}); !slices.Equal(got, []string{"report"}) {
		t.Fatalf("missingRequiredTools() = %v, want latest failed report only", got)
	}
}

func TestMissingRequiredToolsRejectsSuccessfulControlledSkip(t *testing.T) {
	unsatisfied := false
	messages := []agentruntime.Message{{
		TurnID: "turn",
		Type:   agentruntime.MessageTypeToolResult,
		ToolResult: &agentruntime.ToolResult{
			Name:             "report",
			Status:           agentruntime.ToolResultSucceeded,
			TriggerSatisfied: &unsatisfied,
		},
	}}
	if got := missingRequiredTools("turn", messages, []string{"report"}); !slices.Equal(got, []string{"report"}) {
		t.Fatalf("missingRequiredTools() = %v, want controlled skip to remain missing", got)
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
	guard := completionGuardWithRequiredTools(base, []string{"report"}, nil, nil)
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

type stepLimitFinalizationModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
	reportAt int
}

func (m *stepLimitFinalizationModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	switch {
	case index == 0:
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "work", Name: "work", Arguments: map[string]any{},
		}}, Finished: true}}, nil
	case index == m.reportAt:
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "report", Name: "report", Arguments: map[string]any{"message": "done"},
		}}, Finished: true}}, nil
	default:
		return scriptedStream{result: provider.StreamResult{Content: "forgot the report tool", Finished: true}}, nil
	}
}

func (m *stepLimitFinalizationModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}

type earlyReportThenWorkModel struct {
	mu       sync.Mutex
	requests int
}

func (m *earlyReportThenWorkModel) Start(_ context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	index := m.requests
	m.requests++
	m.mu.Unlock()
	switch index {
	case 0:
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "early-report", Name: "report", Arguments: map[string]any{"message": "still working"},
		}}, Finished: true}}, nil
	case 1:
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "work", Name: "work", Arguments: map[string]any{},
		}}, Finished: true}}, nil
	case 2:
		return scriptedStream{result: provider.StreamResult{Content: "work is complete", Finished: true}}, nil
	default:
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "final-report", Name: "report", Arguments: map[string]any{"message": "finished"},
		}}, Finished: true}}, nil
	}
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
	return scriptedStream{result: provider.StreamResult{Content: "done", Finished: true}}, nil
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
	if m.starts == 2+m.repairMisses {
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "report-repair", Name: "report", Arguments: map[string]any{"message": "done"}}}, Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{Content: "done", Finished: true}}, nil
}

func (m *requiredTriggerToolModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}
