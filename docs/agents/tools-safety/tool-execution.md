# Tool execution

`toolexecution.Tool` combines a provider-neutral definition, JSON handler, turn behavior, optional permission descriptor, optional policy-aware permission descriptor, and optional confirmation descriptor. `Registry.Register` requires unique names, handlers, supported turn behavior, and object JSON schemas. `agentcli.WithCustomTool` is the preferred typed wrapper: it infers schema, strictly decodes arguments, invokes a typed handler, and encodes output.

`Executor.Run` consumes runtime requests, applies admission, dispatches accepted work through a bounded worker pool, emits correlated results, and consumes exact-turn interrupts. Calls are keyed by session, turn, and call ID. Preserve these identities and never let one session's interrupt or result affect another.

`ContinueTurn` is the zero-value default. `EndTurn` is attached to a successful
result and tells AgentRuntime to persist the complete result batch, emit
`RunCompleted`, and skip the next provider step. Non-success results always
continue to the provider. Typed tools select this with
`agentcli.ToolTurnBehavior(agentcli.EndTurn)`; raw tools set
`toolexecution.Tool.TurnBehavior`. The framework assigns `EndTurn` to
`start_subagent` and `send_subagent_message` so callback turns remain the only
authoritative child responses. `start_subagent` overrides to `ContinueTurn`
only for `selection_required`, where no dispatch occurred.

Framework tools (`load_skill` and root-only subagent tools) are owned by `toolexecution` and wired by `agentcli`. Application tools remain caller-owned; the framework does not silently register filesystem or shell tools.

Back to [tools-safety/index.md](index.md).
