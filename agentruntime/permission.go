package agentruntime

import (
	"github.com/mrbryside/agentcli/permission"
	"time"
)

type pendingPermission struct {
	run        *Run
	request    permission.Request
	forwarding bool
	done       chan struct{}
	// heldToolResults are received after the executor has the decision but
	// before the runtime commits AgentPermissionResolved. They are released
	// only after that event, preserving causal event order without blocking the
	// shared result router.
	heldToolResults []ToolResultEnvelope
}

func (r *Runtime) routePermissionRequests() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case request, ok := <-r.permissionRequests:
			if !ok {
				return
			}
			r.mu.Lock()
			run := r.active[request.SessionID]
			if run != nil && run.TurnID() == request.TurnID {
				r.pendingPermissions[request.ID] = &pendingPermission{
					run:     run,
					request: clonePermissionRequest(request),
					done:    make(chan struct{}),
				}
			}
			r.mu.Unlock()
			if run != nil && run.TurnID() == request.TurnID {
				run.publish(AgentEvent{Type: AgentPermissionRequested, Permission: &request})
				if request.ExpiresAt != nil {
					go r.expirePermission(request)
				}
			}
		}
	}
}

func (r *Runtime) expirePermission(request permission.Request) {
	timer := time.NewTimer(time.Until(*request.ExpiresAt))
	defer timer.Stop()
	select {
	case <-r.ctx.Done():
		return
	case <-timer.C:
	}
	for {
		r.mu.Lock()
		pending := r.pendingPermissions[request.ID]
		if pending == nil {
			r.mu.Unlock()
			return
		}
		if pending.forwarding {
			done := pending.done
			r.mu.Unlock()
			select {
			case <-done:
				continue
			case <-r.ctx.Done():
				return
			}
		}
		delete(r.pendingPermissions, request.ID)
		close(pending.done)
		run := pending.run
		full := clonePermissionRequest(pending.request)
		r.mu.Unlock()
		run.publish(AgentEvent{Type: AgentPermissionExpired, Permission: &full})
		return
	}
}

func (r *Runtime) cancelRunPermissions(run *Run) {
	if run == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, pending := range r.pendingPermissions {
		if pending.run == run {
			delete(r.pendingPermissions, id)
			if pending.forwarding {
				close(pending.done)
			}
			request := clonePermissionRequest(pending.request)
			run.publish(AgentEvent{Type: AgentPermissionCancelled, Permission: &request})
		}
	}
}

func clonePermissionRequest(request permission.Request) permission.Request {
	clone := request
	clone.Actions = append([]permission.Action(nil), request.Actions...)
	if request.ExpiresAt != nil {
		value := *request.ExpiresAt
		clone.ExpiresAt = &value
	}
	return clone
}
