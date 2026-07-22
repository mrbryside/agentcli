package toolexecution

import (
	"context"

	"harness-api/permission"
)

type permissionPolicyContextKey struct{}

// WithPermissionPolicy attaches an immutable admission-policy snapshot to a
// tool-handler context. Executors use it for every dispatched job. It is
// exported for integrations that wrap or dispatch handlers themselves.
func WithPermissionPolicy(ctx context.Context, policy permission.Policy) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, permissionPolicyContextKey{}, clonePolicyValue(policy))
}

// PermissionPolicyFromContext returns the immutable admission-policy snapshot
// attached by WithPermissionPolicy.
func PermissionPolicyFromContext(ctx context.Context) (permission.Policy, bool) {
	if ctx == nil {
		return permission.Policy{}, false
	}
	policy, ok := ctx.Value(permissionPolicyContextKey{}).(permission.Policy)
	if !ok {
		return permission.Policy{}, false
	}
	return clonePolicyValue(policy), true
}
