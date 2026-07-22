package agentruntime

import (
	"context"
	"time"

	"harness-api/confirmation"
)

type pendingConfirmation struct {
	run        *Run
	request    confirmation.Request
	forwarding bool
	done       chan struct{}
	// Results racing ahead of the resolved event are released only after the
	// event is committed, preserving causal event order.
	heldToolResults []ToolResultEnvelope
}

func (r *Runtime) routeConfirmationRequests() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case request, ok := <-r.confirmationRequests:
			if !ok {
				return
			}
			r.mu.Lock()
			run := r.active[request.SessionID]
			if run != nil && run.TurnID() == request.TurnID {
				r.pendingConfirmations[request.ID] = &pendingConfirmation{run: run, request: cloneConfirmationRequest(request), done: make(chan struct{})}
			}
			r.mu.Unlock()
			if run != nil && run.TurnID() == request.TurnID {
				run.publish(AgentEvent{Type: AgentConfirmationRequested, Confirmation: &request})
				if request.ExpiresAt != nil {
					go r.expireConfirmation(request)
				}
			}
		}
	}
}

// ResolveConfirmation accepts one correlated Yes/No answer. It is
// idempotent for an identical repeated decision and independent of permission
// policy and mode.
func (r *Runtime) ResolveConfirmation(ctx context.Context, decision confirmation.Decision) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.confirmationDecisions == nil {
		return confirmation.ErrClosed
	}
	if err := confirmation.ValidateDecision(decision); err != nil {
		return err
	}
	for {
		r.mu.Lock()
		if prior, exists := r.confirmationDecisionsSeen[decision.ConfirmationID]; exists {
			r.mu.Unlock()
			if prior == decision {
				return nil
			}
			return confirmation.ErrAlreadyResolved
		}
		pending := r.pendingConfirmations[decision.ConfirmationID]
		if pending == nil {
			r.mu.Unlock()
			return confirmation.ErrNotFound
		}
		request := pending.request
		if request.SessionID != decision.SessionID || request.TurnID != decision.TurnID || request.CallID != decision.CallID {
			r.mu.Unlock()
			return confirmation.ErrNotFound
		}
		if request.ExpiresAt != nil && !request.ExpiresAt.After(time.Now()) {
			delete(r.pendingConfirmations, decision.ConfirmationID)
			close(pending.done)
			run := pending.run
			r.mu.Unlock()
			run.publish(AgentEvent{Type: AgentConfirmationExpired, Confirmation: &request})
			return confirmation.ErrClosed
		}
		if pending.forwarding {
			done := pending.done
			r.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		pending.forwarding = true
		r.mu.Unlock()
		err := r.forwardConfirmationDecision(ctx, decision)
		r.mu.Lock()
		current := r.pendingConfirmations[decision.ConfirmationID]
		if current == pending {
			pending.forwarding = false
			close(pending.done)
			pending.done = make(chan struct{})
		}
		if err != nil {
			r.mu.Unlock()
			return err
		}
		if current != pending {
			r.mu.Unlock()
			return confirmation.ErrClosed
		}
		delete(r.pendingConfirmations, decision.ConfirmationID)
		r.confirmationDecisionsSeen[decision.ConfirmationID] = decision
		run := pending.run
		run.publish(AgentEvent{Type: AgentConfirmationResolved, Confirmation: &request, ConfirmationDecision: &decision})
		heldResults := pending.heldToolResults
		r.mu.Unlock()
		for _, result := range heldResults {
			run.enqueueToolResult(result)
		}
		return nil
	}
}

func (r *Runtime) forwardConfirmationDecision(ctx context.Context, decision confirmation.Decision) (err error) {
	defer func() {
		if recover() != nil {
			err = confirmation.ErrClosed
		}
	}()
	select {
	case r.confirmationDecisions <- decision:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.ctx.Done():
		return confirmation.ErrClosed
	}
}

func (r *Runtime) expireConfirmation(request confirmation.Request) {
	timer := time.NewTimer(time.Until(*request.ExpiresAt))
	defer timer.Stop()
	select {
	case <-r.ctx.Done():
		return
	case <-timer.C:
	}
	for {
		r.mu.Lock()
		pending := r.pendingConfirmations[request.ID]
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
		delete(r.pendingConfirmations, request.ID)
		close(pending.done)
		run := pending.run
		full := cloneConfirmationRequest(pending.request)
		r.mu.Unlock()
		run.publish(AgentEvent{Type: AgentConfirmationExpired, Confirmation: &full})
		return
	}
}

func (r *Runtime) cancelRunConfirmations(run *Run) {
	if run == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, pending := range r.pendingConfirmations {
		if pending.run == run {
			delete(r.pendingConfirmations, id)
			if pending.forwarding {
				close(pending.done)
			}
			request := cloneConfirmationRequest(pending.request)
			run.publish(AgentEvent{Type: AgentConfirmationCancelled, Confirmation: &request})
		}
	}
}

func cloneConfirmationRequest(request confirmation.Request) confirmation.Request {
	clone := request
	if request.ExpiresAt != nil {
		expiresAt := *request.ExpiresAt
		clone.ExpiresAt = &expiresAt
	}
	return clone
}
