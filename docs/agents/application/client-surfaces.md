# Client surfaces

`Agent.RunTerminal` is the reusable reference UI. Terminal options select input, output, initial prompt, and session ID. It renders streaming content, tools, permissions, confirmations, child views, and loading state. Exiting the terminal does not close the Agent, allowing later direct turns or server startup.

`Agent.RunServer` and `NewServer` expose Echo JSON/SSE endpoints. The server binds to loopback by default, accepts middleware, limits request size, emits heartbeat comments, queues a bounded number of same-session turns, and lets different sessions proceed concurrently. `NewServer` is preferred when embedding `Handler` or `Echo` in another service.

Both surfaces operate on the same Agent semantics: transcripts are read separately from run events; permission and confirmation decisions require exact IDs; interruptions target a session and turn; subagent ownership is scoped to the parent session.

Back to [application/index.md](index.md).
