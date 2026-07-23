# Client surfaces

`Agent.RunTerminal` is the reusable Terminal UI. Terminal options select input, output, initial prompt, and session ID. It renders streaming content, tools, permissions, confirmations, child views, and loading state. Interactive input and output share one prompt-aware renderer; see [terminal-ui.md](terminal-ui.md) for its editing, streaming, reasoning, and interrupt contracts. Exiting the Terminal UI does not close the Agent, allowing later direct turns or server startup.

`Agent.RunServer` and `NewServer` expose Echo JSON/SSE endpoints. The server binds to loopback by default, accepts middleware, limits request size, emits heartbeat comments, queues a bounded number of same-session turns, and lets different sessions proceed concurrently. `NewServer` is preferred when embedding `Handler` or `Echo` in another service.

Both surfaces operate on the same Agent semantics: transcripts are read separately from run events; permission and confirmation decisions require exact IDs; interruptions target a session and turn; subagent ownership is scoped to the parent session.

The Terminal subscribes to parent-addressed child permission and confirmation
events and renders them in the main session even when the child view is not
open. The HTTP server publishes the same lifecycles as retained
`subagent_permission` and `subagent_confirmation` records on the parent session
SSE stream. Clients that attach after request creation recover with
`GET /v1/sessions/{parentSessionID}/subagent-permissions` and
`GET /v1/sessions/{parentSessionID}/subagent-confirmations`, then resolve using
the existing nested child decision endpoints.

Child views have two equivalent integration paths. Remote applications use the Echo child-record/message endpoints and retained per-turn SSE streams. In-process Go applications use `ListSubagents`, `ListMessages`, `SubagentRun`, and the run subscribe-then-replay fence directly. UI transcript reads must use `ListMessages`; `ReadSubagent` advances the parent model's observation cursor and is not a rendering API. Switching views changes visible state only and must not cancel background child streams.

Back to [application/index.md](index.md).
