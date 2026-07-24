package agentcli

import (
	"context"
	"testing"

	"github.com/mrbryside/agentcli/permission"
)

func TestToolInvocationContextRoundTrip(t *testing.T) {
	want := ToolInvocation{SessionID: "session", TurnID: "turn", CallID: "call", ToolName: "report_discord"}
	got, ok := ToolInvocationFromContext(WithToolInvocation(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("invocation = %#v, ok = %t; want %#v, true", got, ok, want)
	}
	if _, ok := ToolInvocationFromContext(context.Background()); ok {
		t.Fatal("found invocation in a plain context")
	}
}

func TestToolPermissionPolicyContextRoundTrip(t *testing.T) {
	want := ToolPermissionPolicy{Mode: permission.CriticalOnly}
	ctx := WithToolPermissionPolicy(context.Background(), want)
	got, ok := ToolPermissionPolicyFromContext(ctx)
	if !ok || got.Mode != want.Mode {
		t.Fatalf("policy = %#v, ok = %t; want %#v, true", got, ok, want)
	}
}
