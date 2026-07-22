package toolexecution

import "context"

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
