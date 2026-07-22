package agentcli

import "errors"

// ErrClosed reports that an operation was attempted after Close began.
var ErrClosed = errors.New("agent is closed")

// Close cancels the executor and waits for its workers to stop. It is safe to
// call repeatedly and returns the executor's terminal error, if any.
func (a *Agent) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		a.closingCancel()
		a.operationMu.Lock()
		defer a.operationMu.Unlock()
		if a.subagents != nil {
			a.closeErr = a.subagents.Close()
		}
		a.cancel()
		if err := a.Wait(); err != nil && a.closeErr == nil {
			a.closeErr = err
		}
	})
	return a.closeErr
}

// Wait blocks until the executor has completed. It does not cancel the agent.
func (a *Agent) Wait() error {
	if a == nil {
		return nil
	}
	<-a.executorDone
	a.executorMu.RLock()
	defer a.executorMu.RUnlock()
	return a.executorErr
}
