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

func TestRequestEndTurnRequiresHandlerContextAndIsIdempotent(t *testing.T) {
	if err := RequestEndTurn(context.Background()); err == nil {
		t.Fatal("RequestEndTurn outside handler context error = nil")
	}
	ctx := withHandlerTurnControl(context.Background())
	if handlerRequestedEndTurn(ctx) {
		t.Fatal("fresh handler context unexpectedly requests turn end")
	}
	if err := RequestEndTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if err := RequestEndTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if !handlerRequestedEndTurn(ctx) {
		t.Fatal("handler turn-end request was not retained")
	}
}
