package agentcli

import "github.com/mrbryside/agentcli/agentruntime"

// continueSubagentCallbacks gives the HTTP transport the same asynchronous
// child-completion behavior as the reference terminal. Callback turns are
// prioritized ahead of queued human turns but never interrupt an active turn.
func (server *Server) continueSubagentCallbacks() {
	callbacks := server.agent.SubscribeSubagentCallbacks(server.context)
	for {
		select {
		case <-server.context.Done():
			return
		case callback, open := <-callbacks:
			if !open {
				return
			}
			_, _, _ = server.submitTurnWithSource(
				server.context,
				agentruntime.Request{
					SessionID: callback.ParentSessionID,
					Message:   callback.RuntimeMessage(),
				},
				ServerTurnSourceSubagentCallback,
				&callback,
				true,
			)
		}
	}
}
