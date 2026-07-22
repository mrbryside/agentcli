package permission_test

import (
	"errors"
	. "github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage/inmemory"
	"testing"
	"time"
)

func TestMemoryLateDecisionAndDecisionIdentity(t *testing.T) {
	store := inmemory.NewPermissionStorage()
	request := Request{ID: "perm_test", SessionID: "s", TurnID: "t", CallID: "c", ToolName: "write", Actions: []Action{FilesystemWrite}, Risk: RiskMedium, CreatedAt: time.Now()}
	if err := store.Create(request); err != nil {
		t.Fatal(err)
	}
	if got := store.Pending("s"); len(got) != 1 || got[0].State != Pending {
		t.Fatalf("pending=%+v", got)
	}
	decision := Decision{PermissionID: request.ID, SessionID: "s", TurnID: "t", CallID: "c", Type: AllowOnce}
	if _, err := store.Resolve(decision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(decision); err != nil {
		t.Fatalf("same decision should be idempotent: %v", err)
	}
	if _, err := store.Resolve(Decision{PermissionID: request.ID, SessionID: "s", TurnID: "t", CallID: "c", Type: Deny}); !errors.Is(err, ErrAlreadyResolved) {
		t.Fatalf("conflict=%v", err)
	}
}

func TestEvaluatePrecedenceAndModes(t *testing.T) {
	request := Request{Actions: []Action{FilesystemWrite}}
	policy := Policy{Mode: Unrestricted, Rules: []Rule{{Actions: []Action{FilesystemWrite}, Outcome: OutcomeAllow}, {Actions: []Action{FilesystemWrite}, Outcome: OutcomeAsk}, {Actions: []Action{FilesystemWrite}, Outcome: OutcomeDeny}}}
	if got := Evaluate(request, policy); got != OutcomeDeny {
		t.Fatalf("got %s", got)
	}
	if got := Evaluate(request, Policy{Mode: AcceptEdits}); got != OutcomeAllow {
		t.Fatalf("accept edits=%s", got)
	}
	if got := Evaluate(request, Policy{Mode: Plan}); got != OutcomeDeny {
		t.Fatalf("plan=%s", got)
	}
	request.Risk = RiskMedium
	if got := Evaluate(request, Policy{Mode: CriticalOnly}); got != OutcomeAllow {
		t.Fatalf("critical-only medium risk=%s", got)
	}
	request.Risk = RiskHigh
	if got := Evaluate(request, Policy{Mode: CriticalOnly}); got != OutcomeAsk {
		t.Fatalf("critical-only high risk=%s", got)
	}
	request.Actions = []Action{FilesystemWrite, SandboxBypass}
	policy = Policy{Mode: Default, Rules: []Rule{{Actions: []Action{FilesystemWrite}, Outcome: OutcomeAllow}}}
	if got := Evaluate(request, policy); got != OutcomeAsk {
		t.Fatalf("partial allow rule granted sandbox bypass: %s", got)
	}
}

func TestMemoryDefensiveCopiesAndExpiryOrdering(t *testing.T) {
	store := inmemory.NewPermissionStorage()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []ID{"perm_b", "perm_a"} {
		expiry := now.Add(time.Second)
		request := Request{ID: id, SessionID: "s", TurnID: "t", CallID: string(id), ToolName: "read", Actions: []Action{FilesystemRead}, Risk: RiskLow, CreatedAt: now, ExpiresAt: &expiry}
		if err := store.Create(request); err != nil {
			t.Fatal(err)
		}
		request.Actions[0] = SandboxBypass
	}
	pending := store.Pending("s")
	if len(pending) != 2 || pending[0].Request.ID != "perm_a" {
		t.Fatalf("pending=%+v", pending)
	}
	pending[0].Request.Actions[0] = SandboxBypass
	got, _ := store.Get("perm_a")
	if got.Request.Actions[0] != FilesystemRead {
		t.Fatalf("mutation escaped: %+v", got)
	}
	if expired := store.Expire(now.Add(time.Second)); len(expired) != 2 || expired[0].State != Expired {
		t.Fatalf("expired=%+v", expired)
	}
}
