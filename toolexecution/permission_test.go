package toolexecution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

func TestPermissionAdmissionDoesNotOccupyWorker(t *testing.T) {
	registry := NewRegistry()
	mustRegister(t, registry, Tool{Definition: agentruntime.ToolDefinition{Name: "guarded", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"guarded":true}`), nil
	}, Permission: func(json.RawMessage) (permission.Description, error) {
		return permission.Description{Actions: []permission.Action{permission.FilesystemWrite}}, nil
	}})
	mustRegister(t, registry, Tool{Definition: agentruntime.ToolDefinition{Name: "free", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"free":true}`), nil
	}})
	requests := make(chan agentruntime.ToolRequest, 2)
	results := make(chan agentruntime.ToolResultEnvelope, 2)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	prompts := make(chan permission.Request, 1)
	decisions := make(chan permission.Decision, 1)
	executor, err := NewExecutor(registry, 1, Config{PermissionEnabled: true, PermissionRequests: prompts, PermissionDecisions: decisions, Policy: permission.Policy{Mode: permission.Default}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- executor.Run(ctx, requests, results, interrupts) }()
	requests <- agentruntime.ToolRequest{SessionID: "s", TurnID: "t", Call: agentruntime.ToolCall{CallID: "one", Name: "guarded", Arguments: json.RawMessage(`{}`)}}
	prompt := <-prompts
	requests <- agentruntime.ToolRequest{SessionID: "s", TurnID: "t", Call: agentruntime.ToolCall{CallID: "two", Name: "free", Arguments: json.RawMessage(`{}`)}}
	select {
	case result := <-results:
		if result.Result.CallID != "two" {
			t.Fatalf("call=%s", result.Result.CallID)
		}
	case <-time.After(time.Second):
		t.Fatal("free call waited for permission")
	}
	decisions <- permission.Decision{PermissionID: prompt.ID, SessionID: "s", TurnID: "t", CallID: "one", Type: permission.AllowOnce}
	select {
	case result := <-results:
		if result.Result.Status != agentruntime.ToolResultSucceeded {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("allowed call did not run")
	}
	close(requests)
	cancel()
	<-done
}

func TestPermissionModeChangeKeepsPendingPromptAndAppliesToNewRequests(t *testing.T) {
	registry := NewRegistry()
	mustRegister(t, registry, Tool{
		Definition: agentruntime.ToolDefinition{Name: "guarded", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ran":true}`), nil
		},
		Permission: func(json.RawMessage) (permission.Description, error) {
			return permission.Description{Actions: []permission.Action{permission.ProcessExecute}, Risk: permission.RiskHigh}, nil
		},
	})
	controller, err := NewPermissionController(permission.Policy{Mode: permission.Default})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan agentruntime.ToolRequest, 3)
	results := make(chan agentruntime.ToolResultEnvelope, 3)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	prompts := make(chan permission.Request, 3)
	decisions := make(chan permission.Decision, 1)
	executor, err := NewExecutor(registry, 1, Config{
		PermissionEnabled:    true,
		PermissionRequests:   prompts,
		PermissionDecisions:  decisions,
		PermissionController: controller,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.Run(ctx, requests, results, interrupts) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	request := func(callID string) agentruntime.ToolRequest {
		return agentruntime.ToolRequest{SessionID: "s", TurnID: "t", Call: agentruntime.ToolCall{CallID: callID, Name: "guarded", Arguments: json.RawMessage(`{}`)}}
	}
	requests <- request("pending")
	var prompt permission.Request
	select {
	case prompt = <-prompts:
	case <-time.After(time.Second):
		t.Fatal("default mode did not request permission")
	}

	if err := controller.SetMode(permission.Unrestricted); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-results:
		t.Fatalf("mode change resolved an existing prompt: %+v", result)
	case <-time.After(20 * time.Millisecond):
	}
	decisions <- permission.Decision{PermissionID: prompt.ID, SessionID: prompt.SessionID, TurnID: prompt.TurnID, CallID: prompt.CallID, Type: permission.AllowOnce}
	select {
	case result := <-results:
		if result.Result.CallID != "pending" || result.Result.Status != agentruntime.ToolResultFailed || !strings.Contains(result.Result.Error, "permission policy changed") {
			t.Fatalf("resolved pending result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("pending request did not fail after its admission policy changed")
	}

	requests <- request("unrestricted")
	select {
	case result := <-results:
		if result.Result.CallID != "unrestricted" || result.Result.Status != agentruntime.ToolResultSucceeded {
			t.Fatalf("unrestricted result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("new request did not use unrestricted mode")
	}
	select {
	case prompt := <-prompts:
		t.Fatalf("unrestricted request unexpectedly asked: %+v", prompt)
	default:
	}

	if err := controller.SetMode(permission.DontAsk); err != nil {
		t.Fatal(err)
	}
	requests <- request("dont-ask")
	select {
	case result := <-results:
		if result.Result.CallID != "dont-ask" || result.Result.Status != agentruntime.ToolResultDenied {
			t.Fatalf("dontAsk result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("new request did not use dontAsk mode")
	}
}

func TestAdmissionSnapshotSpansDescriptionEvaluationAndWorker(t *testing.T) {
	controller, err := NewPermissionController(permission.Policy{Mode: permission.Default})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	mustRegister(t, registry, Tool{
		Definition: agentruntime.ToolDefinition{Name: "guarded", InputSchema: json.RawMessage(`{"type":"object"}`)},
		PermissionWithPolicy: func(_ json.RawMessage, policy permission.Policy) (permission.Description, error) {
			if policy.Mode != permission.Default {
				return permission.Description{}, fmt.Errorf("descriptor policy = %q, want default", policy.Mode)
			}
			if err := controller.SetMode(permission.CriticalOnly); err != nil {
				return permission.Description{}, err
			}
			return permission.Description{Actions: []permission.Action{permission.FilesystemRead}, Risk: permission.RiskLow}, nil
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, fmt.Errorf("handler ran after permission policy changed")
		},
	})
	requests := make(chan agentruntime.ToolRequest, 1)
	results := make(chan agentruntime.ToolResultEnvelope, 1)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	prompts := make(chan permission.Request, 1)
	decisions := make(chan permission.Decision, 1)
	executor, err := NewExecutor(registry, 1, Config{PermissionEnabled: true, PermissionRequests: prompts, PermissionDecisions: decisions, PermissionController: controller})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.Run(ctx, requests, results, interrupts) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	requests <- agentruntime.ToolRequest{SessionID: "s", TurnID: "t", Call: agentruntime.ToolCall{CallID: "call", Name: "guarded", Arguments: json.RawMessage(`{}`)}}
	var prompt permission.Request
	select {
	case prompt = <-prompts:
	case <-time.After(time.Second):
		t.Fatal("mode change during description let request bypass default prompt")
	}
	if got := controller.Policy().Mode; got != permission.CriticalOnly {
		t.Fatalf("controller mode = %q, want criticalOnly", got)
	}
	decisions <- permission.Decision{PermissionID: prompt.ID, SessionID: prompt.SessionID, TurnID: prompt.TurnID, CallID: prompt.CallID, Type: permission.AllowOnce}
	select {
	case result := <-results:
		if result.Result.Status != agentruntime.ToolResultFailed || !strings.Contains(result.Result.Error, "permission policy changed") {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("admitted request did not fail after policy transition")
	}
}

func TestQueuedUnrestrictedRequestFailsAfterRestrictiveModeChange(t *testing.T) {
	controller, err := NewPermissionController(permission.Policy{Mode: permission.Unrestricted})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	admitted := make(chan struct{})
	mustRegister(t, registry, Tool{
		Definition: agentruntime.ToolDefinition{Name: "block", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			close(started)
			<-release
			return json.RawMessage(`{}`), nil
		},
	})
	mustRegister(t, registry, Tool{
		Definition: agentruntime.ToolDefinition{Name: "outside", InputSchema: json.RawMessage(`{"type":"object"}`)},
		PermissionWithPolicy: func(_ json.RawMessage, policy permission.Policy) (permission.Description, error) {
			if policy.Mode != permission.Unrestricted {
				return permission.Description{}, fmt.Errorf("admission mode = %q, want unrestricted", policy.Mode)
			}
			close(admitted)
			return permission.Description{Actions: []permission.Action{permission.FilesystemRead}, Risk: permission.RiskLow}, nil
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"outside":true}`), nil
		},
	})
	requests := make(chan agentruntime.ToolRequest, 2)
	results := make(chan agentruntime.ToolResultEnvelope, 2)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	executor, err := NewExecutor(registry, 1, Config{PermissionController: controller})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.Run(ctx, requests, results, interrupts) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	requests <- agentruntime.ToolRequest{SessionID: "s", TurnID: "t", Call: agentruntime.ToolCall{CallID: "block", Name: "block", Arguments: json.RawMessage(`{}`)}}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocker did not start")
	}
	requests <- agentruntime.ToolRequest{SessionID: "s", TurnID: "t", Call: agentruntime.ToolCall{CallID: "outside", Name: "outside", Arguments: json.RawMessage(`{}`)}}
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("unrestricted request was not admitted")
	}
	if err := controller.SetMode(permission.Default); err != nil {
		t.Fatal(err)
	}
	close(release)
	for range 2 {
		select {
		case result := <-results:
			if result.Result.CallID == "outside" && (result.Result.Status != agentruntime.ToolResultFailed || !strings.Contains(result.Result.Error, "permission policy changed")) {
				t.Fatalf("queued unrestricted request ran after restrictive transition: %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("missing result")
		}
	}
}

func TestEnabledPermissionRequiresPairedBufferedTransport(t *testing.T) {
	registry := NewRegistry()
	if _, err := NewExecutor(registry, 1, Config{PermissionEnabled: true}); err == nil {
		t.Fatal("expected transport configuration error")
	}
}

func TestPermissionNonInteractiveDeniesPromptInsteadOfWaiting(t *testing.T) {
	registry := NewRegistry()
	mustRegister(t, registry, Tool{
		Definition: agentruntime.ToolDefinition{Name: "critical", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ran":true}`), nil
		},
		Permission: func(json.RawMessage) (permission.Description, error) {
			return permission.Description{Actions: []permission.Action{permission.ProcessExecute}, Risk: permission.RiskHigh}, nil
		},
	})
	requests := make(chan agentruntime.ToolRequest, 1)
	results := make(chan agentruntime.ToolResultEnvelope, 1)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	prompts := make(chan permission.Request, 1)
	decisions := make(chan permission.Decision, 1)
	executor, err := NewExecutor(registry, 1, Config{
		PermissionEnabled:   true,
		NonInteractive:      true,
		PermissionRequests:  prompts,
		PermissionDecisions: decisions,
		Policy:              permission.Policy{Mode: permission.CriticalOnly},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- executor.Run(ctx, requests, results, interrupts) }()
	requests <- agentruntime.ToolRequest{SessionID: "s", TurnID: "t", Call: agentruntime.ToolCall{CallID: "c", Name: "critical", Arguments: json.RawMessage(`{}`)}}
	select {
	case result := <-results:
		if result.Result.Status != agentruntime.ToolResultDenied {
			t.Fatalf("status=%s", result.Result.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("non-interactive permission waited for input")
	}
	if len(prompts) != 0 {
		t.Fatal("non-interactive execution published a prompt")
	}
	cancel()
	<-done
}

func TestPermissionAllowSessionReusedButOtherSessionIsolated(t *testing.T) {
	grants := []permissionGrant{{scope: permission.AllowSession, sessionID: "one", actions: []permission.Action{permission.FilesystemWrite}}}
	request := permission.Request{Actions: []permission.Action{permission.FilesystemWrite}, SessionID: "one"}
	if !hasGrant(grants, request, "project") {
		t.Fatal("same session grant missing")
	}
	request.SessionID = "two"
	if hasGrant(grants, request, "project") {
		t.Fatal("grant leaked session")
	}
}

func TestPermissionAllowProjectScopedToProjectID(t *testing.T) {
	grants := []permissionGrant{{scope: permission.AllowProject, projectID: "one", actions: []permission.Action{permission.FilesystemWrite}}}
	request := permission.Request{Actions: []permission.Action{permission.FilesystemWrite}}
	if !hasGrant(grants, request, "one") {
		t.Fatal("project grant missing")
	}
	if hasGrant(grants, request, "two") {
		t.Fatal("grant leaked project")
	}
}

func TestPermissionExpiryDeniesAllExpiredPending(t *testing.T) {
	store := inmemory.NewPermissionStorage()
	now := time.Now()
	for _, id := range []permission.ID{"a", "b"} {
		expiry := now.Add(time.Millisecond)
		if err := store.Create(permission.Request{ID: id, SessionID: "s", TurnID: "t", CallID: string(id), ToolName: "x", Actions: []permission.Action{permission.FilesystemWrite}, Risk: permission.RiskMedium, CreatedAt: now, ExpiresAt: &expiry}); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.Expire(now.Add(time.Millisecond)); len(got) != 2 {
		t.Fatalf("expired=%d", len(got))
	}
}

func mustRegister(t *testing.T, registry *Registry, tool Tool) {
	t.Helper()
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
}
