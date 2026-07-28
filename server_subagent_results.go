package agentcli

import "github.com/mrbryside/agentcli/agentruntime"

// continueSubagentResults gives the HTTP transport the same asynchronous
// subagent-completion behavior as the reference terminal. An active compatible
// main agent receives the result at its next provider boundary; otherwise a
// result turn is prioritized ahead of queued human turns.
func (server *Server) continueSubagentResults() {
	results := server.agent.SubscribeSubagentResults(server.context)
	for {
		select {
		case <-server.context.Done():
			return
		case result, open := <-results:
			if !open {
				return
			}
			if injected, _ := server.agent.TryInjectSubagentResult(server.context, result); injected {
				continue
			}
			_, _, _ = server.submitTurnWithSource(
				server.context,
				agentruntime.Request{
					SessionID: result.MainAgentSessionID,
					Message:   result.RuntimeMessage(),
				},
				ServerTurnSourceSubagentResult,
				&result,
				true,
			)
		}
	}
}
