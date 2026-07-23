package agentcli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage/inmemory"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestSubagentConfirmationIsPublishedToParentAndDurablyRecoverable(t *testing.T) {
	manager := newTestSubagentManager(t, newConfirmationModel(), 1)
	confirmations := inmemory.NewConfirmationStorage()
	manager.config.confirmations = confirmations
	var executed atomic.Int32
	manager.childFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(),
			withChildAgent(),
			WithModel(newConfirmationModel()),
			WithMessageStorage(manager.config.messages),
			WithPermissionStorage(manager.config.permissions),
			WithConfirmationStorage(confirmations),
			WithTool(confirmationTool(func() { executed.Add(1) })),
		)
	}

	events := manager.subscribeConfirmations(context.Background())
	child, err := manager.Start(context.Background(), "parent-session", "parent-turn", "researcher", "publish", "")
	if err != nil {
		t.Fatal(err)
	}
	requested := waitSubagentConfirmationEvent(t, events, SubagentConfirmationRequested)
	if requested.ParentSessionID != "parent-session" || requested.SubagentID != child.ID || requested.Request == nil {
		t.Fatalf("requested event = %#v", requested)
	}
	pending, err := manager.pendingConfirmations(context.Background(), "parent-session")
	if err != nil || len(pending) != 1 || pending[0].Request.ID != requested.Request.ID {
		t.Fatalf("pending = %#v, err=%v", pending, err)
	}

	instance, err := manager.instance(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision := confirmation.Decision{
		ConfirmationID: requested.Request.ID,
		SessionID:      requested.Request.SessionID,
		TurnID:         requested.Request.TurnID,
		CallID:         requested.Request.CallID,
		Answer:         confirmation.Yes,
	}
	if err := instance.agent.ResolveConfirmation(context.Background(), decision); err != nil {
		t.Fatal(err)
	}
	resolved := waitSubagentConfirmationEvent(t, events, SubagentConfirmationResolved)
	if resolved.Decision == nil || resolved.Decision.ConfirmationID != decision.ConfirmationID {
		t.Fatalf("resolved event = %#v", resolved)
	}
	deadline := time.Now().Add(time.Second)
	for executed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if executed.Load() != 1 {
		t.Fatalf("handler executions = %d", executed.Load())
	}
	pending, err = manager.pendingConfirmations(context.Background(), "parent-session")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after resolve = %#v, err=%v", pending, err)
	}
}

func TestSubagentPermissionIsPublishedToParentAndDurablyRecoverable(t *testing.T) {
	manager := newTestSubagentManager(t, newConfirmationModel(), 1)
	var executed atomic.Int32
	manager.childFactory = func(SubagentDefinition) (*Agent, error) {
		tool := confirmationTool(func() { executed.Add(1) })
		tool.Confirmation = nil
		tool.Permission = toolexecution.StaticPermission(toolexecution.PermissionConfig{
			Actions: []permission.Action{permission.NetworkAccess},
			Risk:    permission.RiskHigh,
			Reason:  "Publishes the child report.",
		})
		return New(context.Background(),
			withChildAgent(),
			WithModel(newConfirmationModel()),
			WithMessageStorage(manager.config.messages),
			WithPermissionStorage(manager.config.permissions),
			WithPermissionPolicy(permission.Policy{Mode: permission.Default}),
			WithTool(tool),
		)
	}

	events := manager.subscribePermissions(context.Background())
	child, err := manager.Start(context.Background(), "parent-session", "parent-turn", "researcher", "publish", "")
	if err != nil {
		t.Fatal(err)
	}
	requested := waitSubagentPermissionEvent(t, events, SubagentPermissionRequested)
	if requested.ParentSessionID != "parent-session" || requested.SubagentID != child.ID || requested.Request == nil {
		t.Fatalf("requested event = %#v", requested)
	}
	pending, err := manager.pendingPermissions(context.Background(), "parent-session")
	if err != nil || len(pending) != 1 || pending[0].Request.ID != requested.Request.ID {
		t.Fatalf("pending = %#v, err=%v", pending, err)
	}

	instance, err := manager.instance(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision := permission.Decision{
		PermissionID: requested.Request.ID,
		SessionID:    requested.Request.SessionID,
		TurnID:       requested.Request.TurnID,
		CallID:       requested.Request.CallID,
		Type:         permission.AllowOnce,
	}
	if err := instance.agent.ResolvePermission(context.Background(), decision); err != nil {
		t.Fatal(err)
	}
	resolved := waitSubagentPermissionEvent(t, events, SubagentPermissionResolved)
	if resolved.Decision == nil || resolved.Decision.PermissionID != decision.PermissionID {
		t.Fatalf("resolved event = %#v", resolved)
	}
	deadline := time.Now().Add(time.Second)
	for executed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if executed.Load() != 1 {
		t.Fatalf("handler executions = %d", executed.Load())
	}
	pending, err = manager.pendingPermissions(context.Background(), "parent-session")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after resolve = %#v, err=%v", pending, err)
	}
}

func waitSubagentConfirmationEvent(t *testing.T, events <-chan SubagentConfirmationEvent, eventType SubagentConfirmationEventType) SubagentConfirmationEvent {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Type == eventType {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for subagent confirmation event %q", eventType)
		}
	}
}

func waitSubagentPermissionEvent(t *testing.T, events <-chan SubagentPermissionEvent, eventType SubagentPermissionEventType) SubagentPermissionEvent {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Type == eventType {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for subagent permission event %q", eventType)
		}
	}
}
