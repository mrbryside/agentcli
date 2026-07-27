package toolexecution

import (
	"context"
	"errors"
	"sync/atomic"
)

// Invocation identifies the runtime tool call currently being handled.
// Handlers that keep session-scoped state can read it with
// InvocationFromContext without depending on executor internals.
type Invocation struct {
	SessionID string
	TurnID    string
	CallID    string
	ToolName  string
}

type invocationContextKey struct{}
type handlerTurnControlContextKey struct{}

type handlerTurnControl struct {
	endRequested atomic.Bool
}

// WithInvocation attaches tool-call identity to a handler context.
func WithInvocation(ctx context.Context, invocation Invocation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, invocationContextKey{}, invocation)
}

// InvocationFromContext returns the identity of the tool call being handled.
func InvocationFromContext(ctx context.Context) (Invocation, bool) {
	if ctx == nil {
		return Invocation{}, false
	}
	invocation, ok := ctx.Value(invocationContextKey{}).(Invocation)
	if !ok || invocation.SessionID == "" || invocation.TurnID == "" || invocation.CallID == "" || invocation.ToolName == "" {
		return Invocation{}, false
	}
	return invocation, true
}

func withHandlerTurnControl(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, handlerTurnControlContextKey{}, &handlerTurnControl{})
}

// RequestEndTurn asks the runtime to end the current turn after this handler
// returns a successful result and every result in the same tool batch
// succeeds. A failed or interrupted handler ignores the request.
func RequestEndTurn(ctx context.Context) error {
	control, ok := handlerTurnControlFromContext(ctx)
	if !ok {
		return errors.New("end-turn request requires an active tool handler context")
	}
	control.endRequested.Store(true)
	return nil
}

func handlerRequestedEndTurn(ctx context.Context) bool {
	control, ok := handlerTurnControlFromContext(ctx)
	return ok && control.endRequested.Load()
}

func handlerTurnControlFromContext(ctx context.Context) (*handlerTurnControl, bool) {
	if ctx == nil {
		return nil, false
	}
	control, ok := ctx.Value(handlerTurnControlContextKey{}).(*handlerTurnControl)
	return control, ok && control != nil
}
