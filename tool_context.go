package agentcli

import (
	"context"

	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/toolexecution"
)

// ToolInvocation identifies the tool call currently being executed. The
// runtime attaches this metadata to the context passed to every tool handler.
type ToolInvocation = toolexecution.Invocation

// ToolInvocationFromContext returns the current tool-call metadata. The
// boolean is false when the context was not created by the tool executor.
func ToolInvocationFromContext(ctx context.Context) (ToolInvocation, bool) {
	return toolexecution.InvocationFromContext(ctx)
}

// WithToolInvocation attaches tool-call metadata to a context. The runtime
// does this automatically; the helper is useful for direct handler tests and
// adapters that invoke a tool outside the executor.
func WithToolInvocation(ctx context.Context, invocation ToolInvocation) context.Context {
	return toolexecution.WithInvocation(ctx, invocation)
}

// ToolPermissionPolicy is the immutable admission-policy snapshot captured
// when a tool request entered execution.
type ToolPermissionPolicy = permission.Policy

// ToolPermissionPolicyFromContext returns the admission policy attached to the
// current tool invocation, when one is available.
func ToolPermissionPolicyFromContext(ctx context.Context) (ToolPermissionPolicy, bool) {
	return toolexecution.PermissionPolicyFromContext(ctx)
}

// WithToolPermissionPolicy attaches an admission policy to a context. The
// executor does this automatically; the helper is useful for direct tests and
// adapters that invoke a handler outside the executor.
func WithToolPermissionPolicy(ctx context.Context, policy ToolPermissionPolicy) context.Context {
	return toolexecution.WithPermissionPolicy(ctx, policy)
}
