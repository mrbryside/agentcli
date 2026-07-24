package toolexecution

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
)

func TestStaticPermissionClonesActionsDefaultsRiskAndPreservesArguments(t *testing.T) {
	actions := []permission.Action{permission.FilesystemRead}
	descriptor := StaticPermission(PermissionConfig{Actions: actions, Reason: "read a custom resource"})
	actions[0] = permission.ProcessExecute

	arguments := json.RawMessage(`{"resource":"report"}`)
	description, err := descriptor(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if description.Risk != permission.RiskMedium || description.Reason != "read a custom resource" || description.Details != string(arguments) {
		t.Fatalf("description = %+v", description)
	}
	if len(description.Actions) != 1 || description.Actions[0] != permission.FilesystemRead {
		t.Fatalf("actions = %v", description.Actions)
	}
	description.Actions[0] = permission.NetworkAccess
	again, err := descriptor(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if again.Actions[0] != permission.FilesystemRead {
		t.Fatalf("descriptor exposed its action slice: %v", again.Actions)
	}
}

func TestStaticPermissionRejectsInvalidRiskAndActions(t *testing.T) {
	for _, config := range []PermissionConfig{
		{Actions: []permission.Action{permission.Action("unknown")}},
		{Actions: []permission.Action{permission.FilesystemRead}, Risk: permission.Risk("unknown")},
		{Risk: permission.RiskMedium},
	} {
		if _, err := StaticPermission(config)(json.RawMessage(`{}`)); err == nil {
			t.Fatalf("config %+v unexpectedly validated", config)
		}
	}
}

func TestStaticPermissionAdmissionModes(t *testing.T) {
	tests := []struct {
		name       string
		policy     permission.Policy
		asks       bool
		wantStatus agentruntime.ToolResultStatus
	}{
		{name: "default asks", policy: permission.Policy{Mode: permission.Default}, asks: true, wantStatus: agentruntime.ToolResultSucceeded},
		{name: "critical only allows medium risk", policy: permission.Policy{Mode: permission.CriticalOnly}, wantStatus: agentruntime.ToolResultSucceeded},
		{name: "unrestricted allows", policy: permission.Policy{Mode: permission.Unrestricted}, wantStatus: agentruntime.ToolResultSucceeded},
		{name: "dont ask denies", policy: permission.Policy{Mode: permission.DontAsk}, wantStatus: agentruntime.ToolResultDenied},
		{name: "plan denies", policy: permission.Policy{Mode: permission.Plan}, wantStatus: agentruntime.ToolResultDenied},
		{name: "explicit deny wins", policy: permission.Policy{Mode: permission.Unrestricted, Rules: []permission.Rule{{Actions: []permission.Action{permission.FilesystemRead}, Outcome: permission.OutcomeDeny}}}, wantStatus: agentruntime.ToolResultDenied},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, prompt, asked := runStaticPermissionTool(t, test.policy, test.asks)
			if asked != test.asks {
				t.Fatalf("asked = %v, want %v", asked, test.asks)
			}
			if asked && (prompt.Risk != permission.RiskMedium || len(prompt.Actions) != 1 || prompt.Actions[0] != permission.FilesystemRead) {
				t.Fatalf("prompt = %+v", prompt)
			}
			if result.Result.Status != test.wantStatus {
				t.Fatalf("result = %+v, want status %s", result, test.wantStatus)
			}
		})
	}
}

func runStaticPermissionTool(t *testing.T, policy permission.Policy, expectPrompt bool) (agentruntime.ToolResultEnvelope, permission.Request, bool) {
	t.Helper()
	registry := NewRegistry()
	mustRegister(t, registry, Tool{
		Definition: agentruntime.ToolDefinition{Name: "custom-read", InputSchema: mustRawToolSchema(`{"type":"object"}`)},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ran":true}`), nil
		},
		Permission: StaticPermission(PermissionConfig{Actions: []permission.Action{permission.FilesystemRead}, Reason: "read a custom resource"}),
	})
	requests := make(chan agentruntime.ToolRequest, 1)
	results := make(chan agentruntime.ToolResultEnvelope, 1)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	prompts := make(chan permission.Request, 1)
	decisions := make(chan permission.Decision, 1)
	executor, err := NewExecutor(registry, 1, Config{PermissionEnabled: true, PermissionRequests: prompts, PermissionDecisions: decisions, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- executor.Run(ctx, requests, results, interrupts) }()

	requests <- agentruntime.ToolRequest{SessionID: "session", TurnID: "turn", Call: agentruntime.ToolCall{CallID: "call", Name: "custom-read", Arguments: json.RawMessage(`{"resource":"report"}`)}}
	var prompt permission.Request
	if expectPrompt {
		select {
		case prompt = <-prompts:
			decisions <- permission.Decision{PermissionID: prompt.ID, SessionID: prompt.SessionID, TurnID: prompt.TurnID, CallID: prompt.CallID, Type: permission.AllowOnce}
		case <-time.After(time.Second):
			t.Fatal("custom tool did not request permission")
		}
	}
	select {
	case result := <-results:
		if !expectPrompt {
			select {
			case prompt := <-prompts:
				t.Fatalf("custom tool unexpectedly requested permission: %+v", prompt)
			default:
			}
		}
		close(requests)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("executor did not stop")
		}
		return result, prompt, expectPrompt
	case <-time.After(time.Second):
		t.Fatal("custom tool did not produce a result")
	}
	return agentruntime.ToolResultEnvelope{}, permission.Request{}, false
}
