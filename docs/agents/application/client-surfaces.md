# Client surfaces

The main model sees `task`, not lifecycle subagent tools. Foreground task output
returns in the current turn; background and `WithTaskForegroundWait` promotion
are delivered exactly once by Agent. Terminal and HTTP clients only observe
the normal run plus `SystemTaskCompleted`/`task_completed`; they do not inject
or continue task results. Persisted subagent views and `/subagents` remain
host session-management interfaces.

`Agent.RunTerminal` is the reusable Terminal UI. Terminal options select input, output, initial prompt, and session ID. It renders streaming content, tools, permissions, confirmations, subagent views, and loading state. Interactive input and output share one prompt-aware renderer; see [terminal-ui.md](terminal-ui.md) for its editing, streaming, reasoning, and interrupt contracts. Exiting the Terminal UI does not close the Agent, allowing later direct turns or server startup.

`Agent.RunServer` and `NewServer` expose Echo JSON/SSE endpoints. The server binds to loopback by default, accepts middleware, limits request size, emits heartbeat comments, queues a bounded number of same-session turns, and lets different sessions proceed concurrently. `NewServer` is preferred when embedding `Handler` or `Echo` in another service.

Both surfaces operate on the same Agent semantics: transcripts are read separately from run events; permission and confirmation decisions require exact IDs; interruptions target a session and turn; subagent ownership is scoped to the main agent session.

The Terminal subscribes to main agent-addressed subagent permission and confirmation
events and renders them in the main session even when the subagent view is not
open. Main-agent/subagent permissions and confirmations share one visible global FIFO;
only the oldest request is actionable, and a shortcut for a different
decision type cannot consume it. The HTTP server publishes the same lifecycles as retained
`subagent_permission` and `subagent_confirmation` records on the main agent session
SSE stream. Clients that attach after request creation recover with
`GET /v1/sessions/{mainAgentSessionID}/subagent-permissions` and
`GET /v1/sessions/{mainAgentSessionID}/subagent-confirmations`, then resolve using
the existing nested subagent decision endpoints.

Successful explicit subagent closes are published
through the generic `SubscribeSystemEvents` stream with type
`SystemSubagentClosed`. The HTTP server retains the same fact as a
`subagent_closed` main agent-session SSE record, including the final subagent
snapshot, previous lifecycle/result status, dropped-message count, and whether the
close interrupted active work.

System events are not persisted as transcript messages. A refreshed client
must hydrate subagent state with `ListSubagents(..., includeClosed=true)` or the
matching HTTP endpoint in addition to loading messages. The server's session
event hub is process-local replay state; durable recovery after restart comes
from `SubagentStorage`.

Subagent views have two equivalent integration paths. Remote applications use the Echo subagent-record/message endpoints and retained per-turn SSE streams. In-process Go applications use `ListSubagents`, `ListMessages`, `SubagentRun`, and the run subscribe-then-replay fence directly. UI transcript reads must use `ListMessages`; `ReadSubagent` advances the main agent model's observation cursor and is not a rendering API. Switching views changes visible state only and must not cancel background subagent streams.

Back to [application/index.md](index.md).
