# Tool execution

`toolexecution.Tool` combines a provider-neutral definition, raw JSON handler,
static turn behavior, optional finalizer marker, optional permission or
policy-aware permission descriptor, and optional confirmation descriptor.
`Registry.Register` requires a unique name, handler, supported behavior, and
object-shaped schema. `RequiredAtTurnEnd` additionally requires `EndTurn`.
Application tools may also configure either `ToolCallGuard` or
`ToolCallGuardPrompt`. Prompt tools optionally select one provider/model with
`*GuardModelConfig`; see [guardrails.md](guardrails.md) for evaluation and
retry behavior.

The root facade exposes `agentcli.Tool`, `ToolDefinition`, `InputSchema`,
permission/confirmation aliases, and turn behavior. `ObjectSchema` builds a
closed schema from a struct of `ToolParameter` descriptors; helpers cover all
JSON scalar types, objects, arrays, descriptions, required fields, and common
constraints. `RawInputSchema` is the validated escape hatch.
`DecodeArguments` strictly decodes one JSON object, rejects unknown fields, and
rejects trailing values. There is no typed custom-tool inference wrapper.

`Executor.Run` applies admission, dispatches through a bounded worker pool,
emits correlated results, and consumes exact-turn interrupts. Calls are keyed
by session, turn, and call ID; that `ToolInvocation` metadata is attached to
handler context after admission. Successful handler output must be valid JSON.
After admission, a tool-call guard can reject the name/arguments before the
handler executes. Rejection becomes a failed correlated result with feedback
for the next model round.

`ContinueTurn` is the default. `EndTurn` skips another provider step only when
the complete result batch succeeded and every result uses `EndTurn`.
Application tools set `Tool.TurnBehavior` directly. Framework start/send and
force-close tools derive behavior from `finish_turn`. `close_subagent` has no
such argument: success and the first controlled lifecycle conflict continue,
while repeating the same child conflict in one parent turn ends the turn to
stop a retry loop.

Required finalizers set both `RequiredAtTurnEnd=true` and
`TurnBehavior=EndTurn`. Only the successful all-end batch immediately before
completion satisfies them; early or mixed calls do not. If the model attempts
to finish while a finalizer is missing, repair rounds keep the normal tool
catalog available and add a reminder naming every missing finalizer. They do
not set provider-specific tool choice or silently narrow the catalog.
There are at most three consecutive no-progress repairs; progress resets the
budget. Exhaustion fails the turn.

Framework tools (`load_skill` and root-only subagent tools) are owned by
`toolexecution`; application tools remain caller-owned. `force_close_subagent`
is not a confirmation tool and is reserved for a specific latest-user
instruction.

Back to [tools-safety/index.md](index.md).
