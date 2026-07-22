package toolexecution

import (
	"context"
	"testing"
)

func TestInvocationContext(t *testing.T) {
	if _, ok := InvocationFromContext(nil); ok {
		t.Fatal("nil context unexpectedly contained an invocation")
	}

	want := Invocation{SessionID: "session", TurnID: "turn", CallID: "call", ToolName: "tool"}
	ctx := WithInvocation(context.Background(), want)
	got, ok := InvocationFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("invocation = %#v, %v; want %#v, true", got, ok, want)
	}

	ctx = WithInvocation(context.Background(), Invocation{SessionID: "session"})
	if _, ok := InvocationFromContext(ctx); ok {
		t.Fatal("incomplete invocation unexpectedly accepted")
	}
}
