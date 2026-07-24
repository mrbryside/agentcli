package toolexecution

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

func TestToolCallFunctionGuardRejectsBeforeHandlerWithRetryableFailedResult(t *testing.T) {
	registry := NewRegistry()
	var observed agentruntime.ToolCallGuardAttempt
	handlerCalls := 0
	guard := func(_ context.Context, attempt agentruntime.ToolCallGuardAttempt) (agentruntime.ToolCallGuardDecision, error) {
		observed = attempt
		attempt.Arguments[0] = '['
		return agentruntime.ToolCallGuardDecision{
			Action:   agentruntime.ToolCallReject,
			Feedback: "query is too broad; call lookup again with a narrower query",
		}, nil
	}
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "lookup", InputSchema: mustRawToolSchema(`{"type":"object"}`)},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			handlerCalls++
			return json.RawMessage(`{"items":[]}`), nil
		},
		TurnBehavior:  EndTurn,
		ToolCallGuard: guard,
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1)
	if err != nil {
		t.Fatal(err)
	}
	result := executeOneTool(t, executor, toolRequest("session", "turn", "call", "lookup", `{"query":"go"}`))
	if result.Result.Status != agentruntime.ToolResultFailed || result.Result.Output != nil {
		t.Fatalf("guarded result = %#v, want failed result without output", result)
	}
	if result.TurnBehavior != ContinueTurn || !strings.Contains(result.Result.Error, "call lookup again") {
		t.Fatalf("guarded result = %#v, want retry feedback and continue behavior", result)
	}
	if observed.SessionID != "session" || observed.TurnID != "turn" || observed.CallID != "call" || observed.ToolName != "lookup" {
		t.Fatalf("guard attempt correlation = %#v", observed)
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0 after rejected tool call", handlerCalls)
	}
}

func TestToolCallFunctionGuardAllowExecutesHandlerAndPreservesTurnBehavior(t *testing.T) {
	registry := NewRegistry()
	handlerCalls := 0
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "finalize", InputSchema: mustRawToolSchema(`{"type":"object"}`)},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			handlerCalls++
			return json.RawMessage(`{"status":"done"}`), nil
		},
		TurnBehavior: EndTurn,
		ToolCallGuard: func(_ context.Context, attempt agentruntime.ToolCallGuardAttempt) (agentruntime.ToolCallGuardDecision, error) {
			attempt.Arguments = append(attempt.Arguments, ' ')
			return agentruntime.ToolCallGuardDecision{Action: agentruntime.ToolCallAllow}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(registry, 1)
	if err != nil {
		t.Fatal(err)
	}
	result := executeOneTool(t, executor, toolRequest("session", "turn", "call", "finalize", `{}`))
	if result.Result.Status != agentruntime.ToolResultSucceeded || string(result.Result.Output) != `{"status":"done"}` || result.TurnBehavior != EndTurn {
		t.Fatalf("guarded result = %#v", result)
	}
	if handlerCalls != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls)
	}
}

func TestToolCallPromptGuardUsesConfiguredModelAndRejectsBeforeHandler(t *testing.T) {
	registry := NewRegistry()
	handlerCalls := 0
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "lookup", InputSchema: mustRawToolSchema(`{"type":"object"}`)},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			handlerCalls++
			return json.RawMessage(`{"items":[]}`), nil
		},
		TurnBehavior:        EndTurn,
		ToolCallGuardPrompt: "Require a narrow query.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecutor(registry, 1); err == nil || !strings.Contains(err.Error(), "guard model") {
		t.Fatalf("NewExecutor() error = %v, want missing guard model", err)
	}
	var typedNilModel *toolGuardModel
	if _, err := NewExecutor(registry, 1, Config{ToolCallGuardModel: typedNilModel}); err == nil || !strings.Contains(err.Error(), "guard model") {
		t.Fatalf("NewExecutor() typed-nil error = %v, want missing guard model", err)
	}
	model := &toolGuardModel{contents: []string{`{"allowed":false,"reason":"broad query","feedback":"call lookup again with a narrower query"}`}}
	executor, err := NewExecutor(registry, 1, Config{ToolCallGuardModel: model})
	if err != nil {
		t.Fatal(err)
	}
	result := executeOneTool(t, executor, toolRequest("session", "turn", "call", "lookup", `{"query":"go"}`))
	if result.Result.Status != agentruntime.ToolResultFailed || result.Result.Output != nil || result.TurnBehavior != ContinueTurn {
		t.Fatalf("prompt-guarded result = %#v", result)
	}
	if !strings.Contains(result.Result.Error, "narrower query") {
		t.Fatalf("prompt-guarded error = %q", result.Result.Error)
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0 after rejected tool call", handlerCalls)
	}
	requests := model.Requests()
	if len(requests) != 1 || len(requests[0].Tools) != 0 || requests[0].ToolChoice == nil || requests[0].ToolChoice.Mode != agentruntime.ToolChoiceNone {
		t.Fatalf("guard request = %#v", requests)
	}
	if len(requests[0].Messages) != 1 || !strings.Contains(requests[0].Messages[0].Content, `"tool_name":"lookup"`) || !strings.Contains(requests[0].Messages[0].Content, `"query":"go"`) || strings.Contains(requests[0].Messages[0].Content, `"output"`) || !strings.Contains(requests[0].SystemPrompts[0], "Require a narrow query") {
		t.Fatalf("guard request = %#v", requests[0])
	}
}

func TestToolCallPromptGuardAllowExecutesHandler(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "finalize", InputSchema: mustRawToolSchema(`{"type":"object"}`)},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"status":"done"}`), nil
		},
		TurnBehavior:        EndTurn,
		ToolCallGuardPrompt: "Allow an empty argument object.",
	}); err != nil {
		t.Fatal(err)
	}
	model := &toolGuardModel{contents: []string{`{"allowed":true,"reason":"valid status","feedback":""}`}}
	executor, err := NewExecutor(registry, 1, Config{ToolCallGuardModel: model})
	if err != nil {
		t.Fatal(err)
	}
	result := executeOneTool(t, executor, toolRequest("session", "turn", "call", "finalize", `{}`))
	if result.Result.Status != agentruntime.ToolResultSucceeded || string(result.Result.Output) != `{"status":"done"}` || result.TurnBehavior != EndTurn {
		t.Fatalf("prompt-guarded result = %#v", result)
	}
}

func TestToolCallPromptGuardResolvesPerToolProviderAndModel(t *testing.T) {
	registry := NewRegistry()
	guardModelConfig := &GuardModelConfig{Provider: "policy", Model: "guard-small"}
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{Name: "lookup", InputSchema: mustRawToolSchema(`{"type":"object"}`)},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"items":["one"]}`), nil
		},
		ToolCallGuardPrompt: "Allow this lookup request.",
		ToolCallGuardModel:  guardModelConfig,
	}); err != nil {
		t.Fatal(err)
	}
	guardModelConfig.Provider = "mutated"
	guardModelConfig.Model = "mutated"
	if _, err := NewExecutor(registry, 1, Config{ToolCallGuardModel: &toolGuardModel{}}); err == nil || !strings.Contains(err.Error(), "no model resolver") {
		t.Fatalf("NewExecutor() error = %v, want missing resolver", err)
	}

	resolvedModel := &toolGuardModel{contents: []string{`{"allowed":true,"reason":"valid","feedback":""}`}}
	var providerName, modelName string
	executor, err := NewExecutor(registry, 1, Config{
		ToolCallGuardModel: &toolGuardModel{},
		ToolCallGuardModelResolver: func(provider, model string) (agentruntime.Model, error) {
			providerName, modelName = provider, model
			return resolvedModel, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if providerName != "policy" || modelName != "guard-small" {
		t.Fatalf("resolved provider/model = %q/%q", providerName, modelName)
	}
	result := executeOneTool(t, executor, toolRequest("session", "turn", "call", "lookup", `{}`))
	if result.Result.Status != agentruntime.ToolResultSucceeded || len(resolvedModel.Requests()) != 1 {
		t.Fatalf("prompt-guarded result = %#v, requests = %d", result, len(resolvedModel.Requests()))
	}
}

func TestToolCallPromptGuardModelResolverFailureRejectsExecutor(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Tool{
		Definition:          agentruntime.ToolDefinition{Name: "lookup", InputSchema: mustRawToolSchema(`{"type":"object"}`)},
		Handler:             testHandler,
		ToolCallGuardPrompt: "check",
		ToolCallGuardModel:  &GuardModelConfig{Provider: "missing", Model: "guard-small"},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := NewExecutor(registry, 1, Config{
		ToolCallGuardModelResolver: func(provider, model string) (agentruntime.Model, error) {
			return nil, errors.New("provider is not configured")
		},
	})
	if err == nil || !strings.Contains(err.Error(), `resolve tool "lookup" prompt guard model`) || !strings.Contains(err.Error(), "provider is not configured") {
		t.Fatalf("NewExecutor() error = %v, want resolver failure", err)
	}
}

func TestToolCallGuardErrorsPreventHandlerAndInvalidHandlerJSONFails(t *testing.T) {
	tests := []struct {
		name    string
		handler Handler
		guard   agentruntime.ToolCallGuard
		want    string
	}{
		{
			name: "invalid handler JSON",
			handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{`), nil
			},
			want: "invalid JSON",
		},
		{
			name: "guard error",
			handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
			guard: func(context.Context, agentruntime.ToolCallGuardAttempt) (agentruntime.ToolCallGuardDecision, error) {
				return agentruntime.ToolCallGuardDecision{}, errors.New("policy unavailable")
			},
			want: "policy unavailable",
		},
		{
			name: "invalid guard decision",
			handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
			guard: func(context.Context, agentruntime.ToolCallGuardAttempt) (agentruntime.ToolCallGuardDecision, error) {
				return agentruntime.ToolCallGuardDecision{Action: agentruntime.ToolCallReject}, nil
			},
			want: "invalid decision",
		},
		{
			name: "guard panic",
			handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
			guard: func(context.Context, agentruntime.ToolCallGuardAttempt) (agentruntime.ToolCallGuardDecision, error) {
				panic("broken policy")
			},
			want: "guard panicked: broken policy",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			handlerCalls := 0
			handler := func(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
				handlerCalls++
				return test.handler(ctx, arguments)
			}
			if err := registry.Register(Tool{
				Definition:    agentruntime.ToolDefinition{Name: "tool", InputSchema: mustRawToolSchema(`{"type":"object"}`)},
				Handler:       handler,
				ToolCallGuard: test.guard,
			}); err != nil {
				t.Fatal(err)
			}
			executor, err := NewExecutor(registry, 1)
			if err != nil {
				t.Fatal(err)
			}
			result := executeOneTool(t, executor, toolRequest("session", "turn", "call", "tool", `{}`))
			if result.Result.Status != agentruntime.ToolResultFailed || result.Result.Output != nil || !strings.Contains(result.Result.Error, test.want) {
				t.Fatalf("result = %#v, want failed result containing %q", result, test.want)
			}
			wantHandlerCalls := 1
			if test.guard != nil {
				wantHandlerCalls = 0
			}
			if handlerCalls != wantHandlerCalls {
				t.Fatalf("handler calls = %d, want %d", handlerCalls, wantHandlerCalls)
			}
		})
	}
}

func executeOneTool(t *testing.T, executor *Executor, request agentruntime.ToolRequest) agentruntime.ToolResultEnvelope {
	t.Helper()
	requests := make(chan agentruntime.ToolRequest, 1)
	results := make(chan agentruntime.ToolResultEnvelope, 1)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := runExecutor(executor, ctx, requests, results, interrupts)
	requests <- request
	result := waitResult(t, results)
	cancel()
	waitDone(t, done)
	return result
}

type toolGuardModel struct {
	mu       sync.Mutex
	contents []string
	requests []agentruntime.ModelRequest
}

func (m *toolGuardModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.contents) == 0 {
		return nil, errors.New("unexpected guard model request")
	}
	content := m.contents[0]
	m.contents = m.contents[1:]
	m.requests = append(m.requests, request)
	return toolGuardStream{content: content}, nil
}

func (m *toolGuardModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}

type toolGuardStream struct{ content string }

func (stream toolGuardStream) Subscribe(context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	events <- provider.StreamEvent{
		Type: provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
			Content: stream.content, Finished: true,
		}},
	}
	close(events)
	return events
}

func (stream toolGuardStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{Content: stream.content, Finished: true}, nil
}
