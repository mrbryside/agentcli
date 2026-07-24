# Tool execution

`toolexecution.Tool` combines a provider-neutral definition, JSON handler, turn behavior, optional permission descriptor, optional policy-aware permission descriptor, and optional confirmation descriptor. `Registry.Register` requires unique names, handlers, supported turn behavior, and an object-shaped `agentruntime.ToolSchema`. The schema is a typed JSON Schema AST and is marshaled only at the provider boundary. Root consumers can use the `agentcli.Tool`, `agentcli.ToolDefinition`, and `agentcli.InputSchema` aliases without importing runtime packages. `agentcli.ObjectSchema` turns a struct of `agentcli.ToolParameter` descriptors into a closed object schema; each descriptor owns its parameter description. `agentcli.RawInputSchema` is the explicit validated escape hatch. `agentcli.WithCustomTool` remains the preferred typed wrapper: it infers schema, strictly decodes arguments, invokes a typed handler, and encodes output.

`Executor.Run` consumes runtime requests, applies admission, dispatches accepted work through a bounded worker pool, emits correlated results, and consumes exact-turn interrupts. Calls are keyed by session, turn, and call ID. Preserve these identities and never let one session's interrupt or result affect another.

`ContinueTurn` is the zero-value default. `EndTurn` is attached to a successful
result and tells AgentRuntime it may skip the next provider step. The runtime
does so only when the complete result batch succeeded and every result uses
`EndTurn`; any continue or non-success result starts another provider step.
Typed tools select static behavior with
`agentcli.ToolTurnBehavior(agentcli.EndTurn)`; raw tools set
`toolexecution.Tool.TurnBehavior`. Framework start/send and force-close tools
derive behavior from their `finish_turn` argument (default true): false is
reserved for planned additional decomposition or dispatch, and true marks the
final/no-more/uncertain case. `start_subagent` overrides to `ContinueTurn` for
`selection_required`, where no dispatch occurred. `close_subagent` has no
`finish_turn` option and always uses `ContinueTurn`, giving the parent one
normal provider round after cleanup.

`agentcli.ToolRequiredAtTurnEnd()` marks a typed custom tool as a finalizer and
also gives it `EndTurn` behavior. Only the successful all-`EndTurn` result
batch immediately preceding completion satisfies it; an earlier invocation in
a continuing round does not. If a turn attempts to complete without a
successful terminal invocation, ordinary provider rounds require *some* tool
while all normal tools remain exposed. This prevents compliant providers from
emitting a text-only answer before the finalizer, without forcing the
finalizer before ordinary work is complete. A repair narrows the tool list to
the missing finalizers and uses a one-shot specific/required choice; its
allowlist remains for later rounds but the forced choice does not. Providers
that ignore tool-choice still rely on the completion guard fallback. The guard
allows up to three consecutive no-progress repair rounds; successful progress
resets that budget. Omitting or failing one after the bounded limit fails the
turn instead of silently violating the requirement.

Framework tools (`load_skill` and root-only subagent tools) are owned by `toolexecution` and wired by `agentcli`. Application tools remain caller-owned; the framework does not silently register filesystem or shell tools.

`force_close_subagent` is an ordinary framework tool, not a confirmation tool.
It bypasses normal child outcome/callback guards, interrupts active work, drops
queued child messages, and retains existing history. Its schema description
and root prompt reserve it for a force-close instruction in the latest user
message; normal autonomous cleanup must use `close_subagent`.

Back to [tools-safety/index.md](index.md).
