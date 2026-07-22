package agentruntime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"encoding/json"
	. "harness-api/agentruntime"
	"harness-api/permission"
	"harness-api/provider"
	"harness-api/storage/inmemory"
	"harness-api/toolexecution"
)

func TestPermissionAllowOnceDelayedAnswerAndModelContinuation(t *testing.T) {
	runtime, _, stop := newPermissionIntegrationRuntime(t)
	defer stop()
	run, err := runtime.Start(context.Background(), Request{SessionID: "s", TurnID: "t", Message: Message{Type: MessageTypeUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := waitRunPrompt(t, run)
	time.Sleep(time.Millisecond)
	if err := resolvePrompt(runtime, prompt, permission.AllowOnce); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for !run.Done() {
		select {
		case <-deadline:
			t.Fatal("run did not continue")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionDenyPersistsDeniedToolResultAndModelContinues(t *testing.T) {
	runtime, _, stop := newPermissionIntegrationRuntime(t)
	defer stop()
	run, err := runtime.Start(context.Background(), Request{SessionID: "s", TurnID: "deny", Message: Message{Type: MessageTypeUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := waitRunPrompt(t, run)
	if err := resolvePrompt(runtime, prompt, permission.Deny); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for !run.Done() {
		select {
		case <-deadline:
			t.Fatal("run did not continue")
		case <-time.After(time.Millisecond):
		}
	}
	result, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].Status != ToolResultDenied {
		t.Fatalf("results=%+v", result.ToolResults)
	}
}

func TestPermissionResolutionPrecedesFastToolResult(t *testing.T) {
	runtime, _, stop := newPermissionIntegrationRuntime(t)
	defer stop()
	run, err := runtime.Start(context.Background(), Request{SessionID: "ordering", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := resolvePrompt(runtime, waitRunPrompt(t, run), permission.AllowOnce); err != nil {
		t.Fatal(err)
	}

	events := collectPermissionEvents(t, run)
	resolved, result := -1, -1
	for index, event := range events {
		switch event.Type {
		case AgentPermissionResolved:
			resolved = index
		case ToolResultReceived:
			result = index
		}
	}
	if resolved < 0 || result < 0 {
		t.Fatalf("events missing permission resolution or tool result: %#v", events)
	}
	if resolved > result {
		t.Fatalf("AgentPermissionResolved index = %d, ToolResultReceived index = %d; events=%#v", resolved, result, events)
	}
}

type permissionScriptModel struct{ starts int }

func (m *permissionScriptModel) Start(context.Context, ModelRequest) (ModelStream, error) {
	m.starts++
	if m.starts == 1 {
		return permissionScriptStream{tool: true}, nil
	}
	return permissionScriptStream{}, nil
}

type permissionScriptStream struct{ tool bool }

func (s permissionScriptStream) Subscribe(context.Context) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent, 1)
	result := provider.StreamResult{Content: "done", Finished: true}
	if s.tool {
		result.CompletedTools = []provider.ToolCall{{ID: "call", Name: "guarded", Arguments: map[string]any{}}}
	}
	ch <- provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: result}}
	close(ch)
	return ch
}
func (permissionScriptStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, nil
}
func newPermissionIntegrationRuntime(t *testing.T) (*Runtime, <-chan permission.Request, func()) {
	t.Helper()
	requests := make(chan ToolRequest, 8)
	results := make(chan ToolResultEnvelope, 8)
	interrupts := make(chan ToolInterrupt, 8)
	prompts := make(chan permission.Request, 8)
	decisions := make(chan permission.Decision, 8)
	registry := toolexecution.NewRegistry()
	if err := registry.Register(toolexecution.Tool{Definition: ToolDefinition{Name: "guarded", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}, Permission: func(json.RawMessage) (permission.Description, error) {
		return permission.Description{Actions: []permission.Action{permission.FilesystemWrite}, Risk: permission.RiskMedium}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	executor, err := toolexecution.NewExecutor(registry, 1, toolexecution.Config{PermissionEnabled: true, PermissionRequests: prompts, PermissionDecisions: decisions, Policy: permission.Policy{Mode: permission.Default}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go executor.Run(ctx, requests, results, interrupts)
	runtime, err := New(ctx, Config{Model: &permissionScriptModel{}, Messages: inmemory.NewMessageStorage(), Tools: registry.Definitions(), ToolRequests: requests, ToolResults: results, ToolInterrupts: interrupts, PermissionRequests: prompts, PermissionDecisions: decisions})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, prompts, func() { cancel() }
}
func waitPermissionPrompt(t *testing.T, ch <-chan permission.Request) permission.Request {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(time.Second):
		t.Fatal("prompt timeout")
		return permission.Request{}
	}
}

func waitRunPrompt(t *testing.T, run *Run) permission.Request {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, event := range run.Events() {
			if event.Type == AgentPermissionRequested && event.Permission != nil {
				return *event.Permission
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime prompt timeout")
	return permission.Request{}
}

func resolvePrompt(runtime *Runtime, prompt permission.Request, kind permission.DecisionType) error {
	deadline := time.Now().Add(time.Second)
	decision := permission.Decision{PermissionID: prompt.ID, SessionID: prompt.SessionID, TurnID: prompt.TurnID, CallID: prompt.CallID, Type: kind}
	for {
		err := runtime.ResolvePermission(context.Background(), decision)
		if err == nil {
			return nil
		}
		if !errors.Is(err, permission.ErrNotFound) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}

func collectPermissionEvents(t *testing.T, run *Run) []AgentEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var events []AgentEvent
	for !run.Done() {
		select {
		case <-ctx.Done():
			t.Fatalf("run did not terminate: %#v", run.Events())
		case <-time.After(time.Millisecond):
		}
	}
	events = run.Events()
	if !run.Done() {
		t.Fatalf("run did not terminate: %#v", run.Events())
	}
	return events
}

func TestPermissionInterruptWhileWaitingRejectsLateDecision(t *testing.T) {
	store := inmemory.NewPermissionStorage()
	request := permission.Request{ID: "perm", SessionID: "s", TurnID: "t", CallID: "c", ToolName: "x", Actions: []permission.Action{permission.FilesystemRead}, Risk: permission.RiskLow, CreatedAt: time.Now()}
	if err := store.Create(request); err != nil {
		t.Fatal(err)
	}
	store.Cancel("s", "t")
	_, err := store.Resolve(permission.Decision{PermissionID: "perm", SessionID: "s", TurnID: "t", CallID: "c", Type: permission.AllowOnce})
	if !errors.Is(err, permission.ErrAlreadyResolved) {
		t.Fatalf("late=%v", err)
	}
}

func TestPermissionExpiryEmitsExactlyOneAndRejectsLateDecision(t *testing.T) {
	store := inmemory.NewPermissionStorage()
	now := time.Now()
	expiry := now.Add(time.Millisecond)
	request := permission.Request{ID: "perm", SessionID: "s", TurnID: "t", CallID: "c", ToolName: "x", Actions: []permission.Action{permission.FilesystemRead}, Risk: permission.RiskLow, CreatedAt: now, ExpiresAt: &expiry}
	if err := store.Create(request); err != nil {
		t.Fatal(err)
	}
	if got := store.Expire(expiry); len(got) != 1 || got[0].State != permission.Expired {
		t.Fatalf("expiry=%+v", got)
	}
	_, err := store.Resolve(permission.Decision{PermissionID: "perm", SessionID: "s", TurnID: "t", CallID: "c", Type: permission.AllowOnce})
	if !errors.Is(err, permission.ErrAlreadyResolved) {
		t.Fatalf("late=%v", err)
	}
}
