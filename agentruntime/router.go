package agentruntime

// routeToolResults is the sole consumer of the caller-owned ToolResults
// channel. It never waits for a run coordinator or subscriber: accepted
// envelopes are appended to a run-local unbounded queue and signalled through
// its size-one notification channel.
func (r *Runtime) routeToolResults() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case envelope, open := <-r.toolResults:
			if !open {
				r.failActiveRunsForClosedResults()
				return
			}
			r.routeToolResult(envelope)
		}
	}
}

func (r *Runtime) routeToolResult(envelope ToolResultEnvelope) {
	r.mu.Lock()
	run := r.active[envelope.SessionID]
	if run != nil && run.TurnID() == envelope.TurnID {
		for _, pending := range r.pendingPermissions {
			request := pending.request
			if request.SessionID == envelope.SessionID && request.TurnID == envelope.TurnID && request.CallID == envelope.Result.CallID {
				pending.heldToolResults = append(pending.heldToolResults, cloneToolResultEnvelope(envelope))
				r.mu.Unlock()
				return
			}
		}
		for _, pending := range r.pendingConfirmations {
			request := pending.request
			if request.SessionID == envelope.SessionID && request.TurnID == envelope.TurnID && request.CallID == envelope.Result.CallID {
				pending.heldToolResults = append(pending.heldToolResults, cloneToolResultEnvelope(envelope))
				r.mu.Unlock()
				return
			}
		}
	}
	r.mu.Unlock()
	if run == nil || run.TurnID() != envelope.TurnID {
		return
	}
	// enqueueToolResult clones the envelope and declines terminal runs, so late
	// worker output cannot alter retained state.
	run.enqueueToolResult(envelope)
}

func (r *Runtime) failActiveRunsForClosedResults() {
	r.mu.Lock()
	if r.toolResultsClosed {
		r.mu.Unlock()
		return
	}
	r.toolResultsClosed = true
	runs := make([]*Run, 0, len(r.active))
	for _, run := range r.active {
		runs = append(runs, run)
	}
	r.mu.Unlock()

	for _, run := range runs {
		run.publish(AgentEvent{Type: RunFailed, Error: ErrToolResultsClosed})
		r.unregister(run)
	}
}
