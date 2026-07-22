# Tool execution

`toolexecution.Tool` combines a provider-neutral definition, JSON handler, turn behavior, optional permission descriptor, optional policy-aware permission descriptor, and optional confirmation descriptor. `Registry.Register` requires unique names, handlers, supported turn behavior, and object JSON schemas. `agentcli.WithCustomTool` is the preferred typed wrapper: it infers schema, strictly decodes arguments, invokes a typed handler, and encodes output.

`Executor.Run` consumes runtime requests, applies admission, dispatches accepted work through a bounded worker pool, emits correlated results, and consumes exact-turn interrupts. Calls are keyed by session, turn, and call ID. Preserve these identities and never let one session's interrupt or result affect another.

`ContinueTurn` is the zero-value default. `EndTurn` is attached to a successful
result and tells AgentRuntime it may skip the next provider step. The runtime
does so only when the complete result batch succeeded and every result uses
`EndTurn`; any continue or non-success result starts another provider step.
Typed tools select static behavior with
`agentcli.ToolTurnBehavior(agentcli.EndTurn)`; raw tools set
`toolexecution.Tool.TurnBehavior`. Framework start/send tools derive behavior
from their `finish_turn` argument (default true): false is reserved for planned
additional decomposition/dispatch, and true marks the final/no-more/uncertain
case. `start_subagent` overrides to `ContinueTurn` for `selection_required`,
where no dispatch occurred.

Framework tools (`load_skill` and root-only subagent tools) are owned by `toolexecution` and wired by `agentcli`. Application tools remain caller-owned; the framework does not silently register filesystem or shell tools.

Back to [tools-safety/index.md](index.md).
