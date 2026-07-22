package toolexecution

import (
	"fmt"
	"sync"
	"sync/atomic"

	"harness-api/permission"
)

// PermissionController is the single immutable policy snapshot shared by an
// executor and its custom tools. Swapping a snapshot changes admission without
// mutating a policy a worker may still be reading.
type PermissionController struct {
	snapshot atomic.Pointer[policySnapshot]
	gate     sync.Mutex
}

// policySnapshot is immutable. Its epoch changes for every real mode
// transition, including transitions back to an earlier mode.
type policySnapshot struct {
	policy permission.Policy
	epoch  uint64
}

// NewPermissionController creates a controller from policy. An empty mode is
// normalized to Default for compatibility with the executor's legacy config.
func NewPermissionController(policy permission.Policy) (*PermissionController, error) {
	if policy.Mode == "" {
		policy.Mode = permission.Default
	}
	if !permission.IsValidMode(policy.Mode) {
		return nil, fmt.Errorf("unknown permission mode %q", policy.Mode)
	}
	controller := &PermissionController{}
	controller.snapshot.Store(&policySnapshot{policy: clonePolicyValue(policy), epoch: 1})
	return controller, nil
}

// Policy returns an independent immutable-policy snapshot.
func (c *PermissionController) Policy() permission.Policy {
	return c.currentSnapshot().policy
}

func (c *PermissionController) currentSnapshot() policySnapshot {
	if c == nil {
		return policySnapshot{policy: permission.Policy{Mode: permission.Default}}
	}
	current := c.snapshot.Load()
	if current == nil {
		return policySnapshot{policy: permission.Policy{Mode: permission.Default}}
	}
	return policySnapshot{policy: clonePolicyValue(current.policy), epoch: current.epoch}
}

func (c *PermissionController) isCurrent(snapshot policySnapshot) bool {
	if c == nil {
		return snapshot.epoch == 0
	}
	current := c.snapshot.Load()
	return current != nil && current.epoch == snapshot.epoch
}

// executeIfCurrent makes the admission-epoch check the handler's logical start
// point, linearizable with SetMode. A later mode change cannot admit queued
// work from an earlier epoch, but it does not wait for already-started handlers
// (which retain normal interruption behavior).
func (c *PermissionController) executeIfCurrent(snapshot policySnapshot, execute func()) bool {
	if c == nil {
		if snapshot.epoch != 0 {
			return false
		}
		execute()
		return true
	}
	c.gate.Lock()
	if !c.isCurrent(snapshot) {
		c.gate.Unlock()
		return false
	}
	c.gate.Unlock()
	execute()
	return true
}

// SetMode atomically replaces the policy mode while retaining every explicit
// rule. Setting the current mode is a no-op.
func (c *PermissionController) SetMode(mode permission.Mode) error {
	if c == nil {
		return fmt.Errorf("permission controller is nil")
	}
	if !permission.IsValidMode(mode) {
		return fmt.Errorf("unknown permission mode %q", mode)
	}
	c.gate.Lock()
	defer c.gate.Unlock()
	current := c.snapshot.Load()
	if current == nil {
		c.snapshot.Store(&policySnapshot{policy: permission.Policy{Mode: mode}, epoch: 1})
		return nil
	}
	if current.policy.Mode == mode {
		return nil
	}
	next := &policySnapshot{policy: clonePolicyValue(current.policy), epoch: current.epoch + 1}
	next.policy.Mode = mode
	c.snapshot.Store(next)
	return nil
}

func clonePolicy(policy permission.Policy) *permission.Policy {
	clone := permission.Policy{Mode: policy.Mode, Rules: make([]permission.Rule, len(policy.Rules))}
	for index, rule := range policy.Rules {
		clone.Rules[index] = permission.Rule{Outcome: rule.Outcome, Actions: append([]permission.Action(nil), rule.Actions...)}
	}
	return &clone
}

func clonePolicyValue(policy permission.Policy) permission.Policy {
	return *clonePolicy(policy)
}
