package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/toolexecution"
)

type customToolTestInput struct {
	Topic string   `json:"topic" description:"Topic to look up" minLength:"1" maxLength:"80"`
	Limit int      `json:"limit,omitempty" minimum:"1" maximum:"20"`
	Tags  []string `json:"tags,omitempty"`
}

type customToolTestOutput struct {
	Summary string `json:"summary"`
}

func TestNewCustomToolInfersSchemaAndTranslatesTypedValues(t *testing.T) {
	tool, err := NewCustomTool("lookup_topic", "Looks up a topic.", func(_ context.Context, input customToolTestInput) (customToolTestOutput, error) {
		return customToolTestOutput{Summary: strings.TrimSpace(input.Topic)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Type                 string                    `json:"type"`
		Properties           map[string]map[string]any `json:"properties"`
		Required             []string                  `json:"required"`
		AdditionalProperties bool                      `json:"additionalProperties"`
	}
	encodedSchema, err := json.Marshal(tool.Definition.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties || len(schema.Required) != 1 || schema.Required[0] != "topic" {
		t.Fatalf("schema = %s", encodedSchema)
	}
	if schema.Properties["topic"]["description"] != "Topic to look up" || schema.Properties["topic"]["minLength"] != float64(1) || schema.Properties["limit"]["maximum"] != float64(20) {
		t.Fatalf("properties = %#v", schema.Properties)
	}

	output, err := tool.Handler(context.Background(), json.RawMessage(`{"topic":"  Go  ","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != `{"summary":"Go"}` {
		t.Fatalf("output = %s", output)
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"topic":"Go","unknown":true}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`null`)); err == nil {
		t.Fatal("handler accepted non-object arguments")
	}
}

func TestNewCustomToolConfiguresTypedPermissionAndConfirmation(t *testing.T) {
	tool, err := NewCustomTool("publish", "Publishes a report.", func(_ context.Context, input customToolTestInput) (customToolTestOutput, error) {
		return customToolTestOutput{Summary: input.Topic}, nil
	},
		ToolPermission(func(input customToolTestInput) (permission.Description, error) {
			return permission.Description{Actions: []permission.Action{permission.NetworkAccess}, Risk: permission.RiskHigh, Reason: "Publish " + input.Topic}, nil
		}),
		ToolConfirmation(func(input customToolTestInput) (confirmation.Description, error) {
			return confirmation.Description{Title: "Publish", Message: "Continue?", Details: "Topic: " + input.Topic}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"topic":"release"}`)
	permissionDescription, err := tool.Permission(arguments)
	if err != nil || permissionDescription.Reason != "Publish release" || permissionDescription.Risk != permission.RiskHigh {
		t.Fatalf("permission = %#v, %v", permissionDescription, err)
	}
	confirmationDescription, err := tool.Confirmation(arguments)
	if err != nil || confirmationDescription.Details != "Topic: release" {
		t.Fatalf("confirmation = %#v, %v", confirmationDescription, err)
	}
}

func TestNewCustomToolSupportsStaticPermissionAndSchemaOverride(t *testing.T) {
	tool, err := NewCustomTool("map_tool", "Accepts a map.", func(_ context.Context, input map[string]string) (map[string]string, error) {
		return input, nil
	},
		ToolSchema(agentruntime.ToolSchema{Type: "object", Properties: map[string]agentruntime.ToolSchema{"value": {Type: "string", Pattern: "^[a-z]+$"}}, Required: []string{"value"}, AdditionalProperties: agentruntime.AdditionalPropertiesBool(false)}),
		StaticToolPermission(toolexecution.PermissionConfig{Actions: []permission.Action{permission.FilesystemRead}, Risk: permission.RiskLow, Reason: "Reads local data."}),
	)
	if err != nil {
		t.Fatal(err)
	}
	encodedSchema, _ := json.Marshal(tool.Definition.InputSchema)
	if !strings.Contains(string(encodedSchema), `"pattern":"^[a-z]+$"`) || tool.Permission == nil {
		t.Fatalf("tool = %#v", tool)
	}
}

func TestNewCustomToolConfiguresTurnBehavior(t *testing.T) {
	tool, err := NewCustomTool("enqueue", "Enqueues work.", func(_ context.Context, input customToolTestInput) (customToolTestOutput, error) {
		return customToolTestOutput{Summary: input.Topic}, nil
	}, ToolTurnBehavior(EndTurn))
	if err != nil {
		t.Fatal(err)
	}
	if tool.TurnBehavior != toolexecution.EndTurn {
		t.Fatalf("turn behavior = %q, want %q", tool.TurnBehavior, toolexecution.EndTurn)
	}
	if _, err := NewCustomTool("invalid-turn", "", func(context.Context, customToolTestInput) (struct{}, error) {
		return struct{}{}, nil
	}, ToolTurnBehavior("later")); err == nil {
		t.Fatal("unsupported turn behavior accepted")
	}
}

func TestNewCustomToolConfiguresRequiredEndTurnFinalizer(t *testing.T) {
	tool, err := NewCustomTool("finalize", "Finalizes the turn.", func(_ context.Context, input customToolTestInput) (customToolTestOutput, error) {
		return customToolTestOutput{Summary: input.Topic}, nil
	}, ToolRequiredAtTurnEnd(), ToolTurnBehavior(ContinueTurn))
	if err != nil {
		t.Fatal(err)
	}
	if !tool.RequiredAtTurnEnd || tool.TurnBehavior != toolexecution.EndTurn {
		t.Fatalf("finalizer metadata = required:%t behavior:%q", tool.RequiredAtTurnEnd, tool.TurnBehavior)
	}
}

func TestWithCustomToolRequiredAtTurnEndRepairsMissingCall(t *testing.T) {
	model := &finalizerRepairModel{}
	var calls atomic.Int32
	agent, err := New(context.Background(),
		WithModel(model),
		WithCustomTool("finalize", "Finalizes the turn.", func(_ context.Context, input customToolTestInput) (customToolTestOutput, error) {
			calls.Add(1)
			return customToolTestOutput{Summary: input.Topic}, nil
		}, ToolRequiredAtTurnEnd()),
	)
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
	if calls.Load() != 1 || run.CompletionRepairCount() != 1 {
		t.Fatalf("finalizer calls=%d repairs=%d", calls.Load(), run.CompletionRepairCount())
	}
	requests := model.Requests()
	if len(requests) != 2 || len(requests[1].Tools) != 1 || requests[1].Tools[0].Name != "finalize" {
		t.Fatalf("repair requests = %#v", requests)
	}
}

func TestWithCustomToolEndTurnSkipsSecondModelCall(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "enqueue-1", Name: "enqueue", Arguments: map[string]any{"topic": "Go"},
	}}}
	agent, err := New(context.Background(),
		WithModel(model),
		WithCustomTool("enqueue", "Enqueues work.", func(_ context.Context, input customToolTestInput) (customToolTestOutput, error) {
			return customToolTestOutput{Summary: input.Topic}, nil
		}, ToolTurnBehavior(EndTurn)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("end-turn-tool"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if got := len(model.Requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1", got)
	}
	result, err := run.Result()
	if err != nil || !result.Finished || len(result.ToolResults) != 1 {
		t.Fatalf("Result() = (%#v, %v)", result, err)
	}
}

func TestNewCustomToolRejectsInvalidConfiguration(t *testing.T) {
	type recursive struct {
		Child *recursive `json:"child,omitempty"`
	}
	if _, err := NewCustomTool("", "", func(context.Context, customToolTestInput) (customToolTestOutput, error) {
		return customToolTestOutput{}, nil
	}); err == nil {
		t.Fatal("empty name accepted")
	}
	var nilHandler func(context.Context, customToolTestInput) (customToolTestOutput, error)
	if _, err := NewCustomTool("nil", "", nilHandler); err == nil {
		t.Fatal("nil handler accepted")
	}
	if _, err := NewCustomTool("scalar", "", func(context.Context, string) (string, error) { return "", nil }); err == nil {
		t.Fatal("scalar input accepted")
	}
	if _, err := NewCustomTool("recursive", "", func(context.Context, recursive) (struct{}, error) { return struct{}{}, nil }); err == nil {
		t.Fatal("recursive input accepted without override")
	}
	if _, err := NewCustomTool("recursive-with-schema", "", func(context.Context, recursive) (struct{}, error) { return struct{}{}, nil }, ToolSchema(agentruntime.ToolSchema{Type: "object", Properties: map[string]agentruntime.ToolSchema{"child": {Type: "object"}}, AdditionalProperties: agentruntime.AdditionalPropertiesBool(false)})); err != nil {
		t.Fatalf("recursive input with schema override: %v", err)
	}
	if _, err := NewCustomTool("bad-schema", "", func(context.Context, customToolTestInput) (struct{}, error) { return struct{}{}, nil }, ToolSchema(agentruntime.ToolSchema{Type: "array"})); err == nil {
		t.Fatal("non-object schema accepted")
	}
	if _, err := NewCustomTool("nil-option", "", func(context.Context, customToolTestInput) (struct{}, error) { return struct{}{}, nil }, nil); err == nil {
		t.Fatal("nil option accepted")
	}
	if _, err := NewCustomTool("nil-confirm", "", func(context.Context, customToolTestInput) (struct{}, error) { return struct{}{}, nil }, ToolConfirmation[customToolTestInput](nil)); err == nil {
		t.Fatal("nil confirmation descriptor accepted")
	}
}

type finalizerRepairModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
}

func (model *finalizerRepairModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	model.mu.Lock()
	index := len(model.requests)
	model.requests = append(model.requests, request)
	model.mu.Unlock()
	if index == 0 {
		return scriptedStream{result: provider.StreamResult{Content: "attempted early finish", Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{
		ID: "finalize-1", Name: "finalize", Arguments: map[string]any{"topic": "done"},
	}}, Finished: true}}, nil
}

func (model *finalizerRepairModel) Requests() []agentruntime.ModelRequest {
	model.mu.Lock()
	defer model.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), model.requests...)
}

func TestWithCustomToolReportsConfigurationDuringAgentInitialization(t *testing.T) {
	_, err := New(context.Background(), WithModel(&scriptedModel{}), WithCustomTool("bad", "", func(context.Context, string) (string, error) {
		return "", errors.New("not reached")
	}))
	if err == nil || !strings.Contains(err.Error(), "input must be a struct or map") {
		t.Fatalf("New error = %v", err)
	}
}

func TestWithCustomToolExecutesInsideAgentLoop(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "lookup-1", Name: "lookup_topic", Arguments: map[string]any{"topic": "Go", "limit": float64(2)},
	}}}
	executed := make(chan customToolTestInput, 1)
	agent, err := New(context.Background(),
		WithModel(model),
		WithCustomTool("lookup_topic", "Looks up a topic.", func(_ context.Context, input customToolTestInput) (customToolTestOutput, error) {
			executed <- input
			return customToolTestOutput{Summary: "found " + input.Topic}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("typed-tool-loop"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	result, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].Status != "succeeded" || string(result.ToolResults[0].Output) != `{"summary":"found Go"}` {
		t.Fatalf("result = %#v", result)
	}
	select {
	case input := <-executed:
		if input.Topic != "Go" || input.Limit != 2 {
			t.Fatalf("typed input = %#v", input)
		}
	default:
		t.Fatal("typed handler was not executed")
	}
}
