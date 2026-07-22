package agentruntime

import (
	"context"
	"testing"

	"harness-api/permission"
)

func TestResolvePermissionPublishesBeforeReleasingHeldToolResult(t *testing.T) {
	run := newRun("session", "turn")
	decisionChannel := make(chan permission.Decision, 1)
	request := permission.Request{ID: "permission", SessionID: "session", TurnID: "turn", CallID: "call", ToolName: "tool"}
	runtime := &Runtime{
		ctx:                     context.Background(),
		active:                  map[string]*Run{"session": run},
		permissionDecisions:     decisionChannel,
		pendingPermissions:      map[permission.ID]*pendingPermission{request.ID: {run: run, request: request, done: make(chan struct{})}},
		permissionDecisionsSeen: make(map[permission.ID]permission.Decision),
	}
	result := ToolResultEnvelope{SessionID: "session", TurnID: "turn", Result: ToolResult{CallID: "call", Name: "tool", Status: ToolResultSucceeded}}

	runtime.routeToolResult(result)
	if got := run.drainToolResults(); len(got) != 0 {
		t.Fatalf("tool result bypassed unresolved permission: %#v", got)
	}

	decision := permission.Decision{PermissionID: request.ID, SessionID: request.SessionID, TurnID: request.TurnID, CallID: request.CallID, Type: permission.AllowOnce}
	if err := runtime.ResolvePermission(context.Background(), decision); err != nil {
		t.Fatal(err)
	}
	if got := <-decisionChannel; got != decision {
		t.Fatalf("forwarded decision = %#v, want %#v", got, decision)
	}
	if events := run.Events(); len(events) != 1 || events[0].Type != AgentPermissionResolved {
		t.Fatalf("events after resolution = %#v, want only AgentPermissionResolved", events)
	}

	released := run.drainToolResults()
	if len(released) != 1 || released[0].Result.CallID != result.Result.CallID {
		t.Fatalf("released results = %#v, want %#v", released, result)
	}
	run.publish(AgentEvent{Type: ToolResultReceived, ToolResult: &released[0]})
	events := run.Events()
	if events[0].Type != AgentPermissionResolved || events[1].Type != ToolResultReceived {
		t.Fatalf("event order = %#v, want resolution before result", events)
	}
}

func TestResolvePermissionDoesNotPublishWhenForwardingFails(t *testing.T) {
	run := newRun("session", "turn")
	decisionChannel := make(chan permission.Decision, 1)
	close(decisionChannel)
	request := permission.Request{ID: "permission", SessionID: "session", TurnID: "turn", CallID: "call", ToolName: "tool"}
	runtime := &Runtime{
		ctx:                     context.Background(),
		active:                  map[string]*Run{"session": run},
		permissionDecisions:     decisionChannel,
		pendingPermissions:      map[permission.ID]*pendingPermission{request.ID: {run: run, request: request, done: make(chan struct{})}},
		permissionDecisionsSeen: make(map[permission.ID]permission.Decision),
	}

	decision := permission.Decision{PermissionID: request.ID, SessionID: request.SessionID, TurnID: request.TurnID, CallID: request.CallID, Type: permission.AllowOnce}
	if err := runtime.ResolvePermission(context.Background(), decision); err != permission.ErrClosed {
		t.Fatalf("ResolvePermission() error = %v, want %v", err, permission.ErrClosed)
	}
	if events := run.Events(); len(events) != 0 {
		t.Fatalf("forwarding failure published events: %#v", events)
	}
}

func TestUnregisterPrunesResolvedPermissionDecisionsForRun(t *testing.T) {
	run := newRun("session", "turn")
	runtime := &Runtime{
		active: map[string]*Run{"session": run},
		permissionDecisionsSeen: map[permission.ID]permission.Decision{
			"same-turn":  {PermissionID: "same-turn", SessionID: "session", TurnID: "turn", CallID: "call", Type: permission.AllowOnce},
			"other-turn": {PermissionID: "other-turn", SessionID: "session", TurnID: "other", CallID: "call", Type: permission.AllowOnce},
		},
	}

	runtime.unregister(run)

	if _, found := runtime.permissionDecisionsSeen["same-turn"]; found {
		t.Fatal("resolved decision for unregistered run was retained")
	}
	if _, found := runtime.permissionDecisionsSeen["other-turn"]; !found {
		t.Fatal("resolved decision for a different turn was removed")
	}
}
