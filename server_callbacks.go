package agentcli

import "github.com/mrbryside/agentcli/agentruntime"

// continueSubagentCallbacks gives the HTTP transport the same asynchronous
// child-completion behavior as the reference terminal. An active compatible
// parent receives the callback at its next provider boundary; otherwise a
// callback turn is prioritized ahead of queued human turns.
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
			if injected, _ := server.agent.TryInjectSubagentCallback(server.context, callback); injected {
				continue
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
