# Tool execution

`toolexecution.Tool` combines a provider-neutral definition, raw JSON handler,
one optional lifecycle mode, optional permission or policy-aware permission
descriptor, and optional confirmation descriptor. `Registry.Register` requires
a unique name, handler, supported lifecycle, and object-shaped schema.
Application tools may also configure either `ToolCallGuard` or
`ToolCallGuardPrompt`. Prompt tools optionally select one provider/model with
`*GuardModelConfig`; see [guardrails.md](guardrails.md) for evaluation and
retry behavior.

The root facade exposes `agentcli.Tool`, `ToolDefinition`, `InputSchema`,
permission/confirmation aliases, and lifecycle modes. `ObjectSchema` builds a
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

The zero lifecycle is the normal immediate handler followed by another provider
round. `Lifecycle: EndTurn` makes the tool a required turn finalizer.
`Lifecycle: EndResponseScope` makes it a required response-scope finalizer.
Application tools configure completion policy only through `Tool.Lifecycle`. Framework
`start_subagent`, `send_subagent_message`, and `force_close_subagent` always
continue. The start contract permits exactly one call per provider round.
Accepted start/send results require a later
callback but allow independent, non-duplicative work before the parent finishes
through its normal response or application finalizer. For `close_subagent`,
success and the first controlled lifecycle conflict continue,
while repeating the same child conflict in one parent turn ends the turn to
stop a retry loop.

`EndTurn` executes its handler immediately. Only the successful all-end batch
immediately before completion satisfies it; early or mixed calls do not. If the model attempts
to finish while a finalizer is missing, repair rounds expose only the missing
finalizer tools and add a reminder naming each one. If a caller-supplied
completion guard also requests a bounded tool allowlist, its tools are merged
with the missing finalizers.
There are at most three consecutive no-progress repairs; progress resets the
budget. Exhaustion fails the turn.

For user-visible delivery, prefer `Lifecycle: EndResponseScope`. A model call
stages the latest arguments and receives a successful JSON result with
`status=deferred`, `delivery=end_response_scope`, active-turn and
pending-callback counts, and `retry_in_current_turn=false`; the handler is not
called during that turn. One user message opens one response scope. Accepted
subagent work keeps it open, callback continuations remain in the same scope,
and completed, failed, or incomplete callbacks all settle one accepted
dispatch. Once every continuation turn has fully completed and no accepted
callback remains, the runtime calls each staged handler exactly once. Callback
replays cannot settle a newer follow-up dispatch.

Framework tools (`load_skill` and root-only subagent tools) are owned by
`toolexecution`; application tools remain caller-owned. `force_close_subagent`
is not a confirmation tool and is reserved for a specific latest-user
instruction.

Back to [tools-safety/index.md](index.md).
