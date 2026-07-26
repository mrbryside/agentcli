# Tool execution

`toolexecution.Tool` combines a provider-neutral definition, raw JSON handler,
one optional trigger mode, optional permission or policy-aware permission
descriptor, and optional confirmation descriptor. `Registry.Register` requires
a unique name, handler, supported trigger, and object-shaped schema.
Application tools may also configure either `ToolCallGuard` or
`ToolCallGuardPrompt`. Prompt tools optionally select one provider/model with
`*GuardModelConfig`; see [guardrails.md](guardrails.md) for evaluation and
retry behavior.

The root facade exposes `agentcli.Tool`, `ToolDefinition`, `InputSchema`,
permission/confirmation aliases, and trigger modes. `ObjectSchema` builds a
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

The zero trigger is the normal immediate handler followed by another provider
round. `Trigger: EndTurn` makes the tool a required turn trigger tool.
`Trigger: EndResponseScope` makes it a required response-scope trigger tool.
`EndTurnOnSuccess: true` is separate from `Trigger`: it keeps the tool optional,
runs its handler immediately, and ends the turn when every result in that tool
batch succeeds. One such tool can end a mixed batch containing normal immediate
tools. It cannot bypass missing required triggers and cannot be combined with
`Trigger`. Framework
`start_subagent`, `send_subagent_message`, and `close_subagent` always
continue. The start contract permits exactly one call per provider round.
Accepted start/send results require a later
callback but allow independent, non-duplicative work before the parent finishes
through its normal response or required trigger tool. `close_subagent` is a
destructive escape hatch for a current explicit human instruction; it is not an
`EndResponseScope` trigger tool or automatic cleanup mechanism.

`EndTurn` executes its handler immediately. Only a successful terminal batch
immediately before completion satisfies it; early or continuing calls do not.
A terminal batch is either all-`EndTurn` or contains a successful
`EndTurnOnSuccess` tool. If the model attempts to finish while a trigger tool
is missing, repair rounds expose only the missing
trigger tools and add a reminder naming each one. If a caller-supplied
completion guard also requests a bounded tool allowlist, its tools are merged
with the missing trigger tools.
There are at most three consecutive no-progress repairs; progress resets the
budget. Exhaustion fails the turn.

For user-visible delivery, prefer `Trigger: EndResponseScope`. A model call
stages the latest arguments and receives a successful JSON result with
`status=deferred`, `delivery=end_response_scope`, active-turn and
pending-callback counts, and `retry_in_current_turn=false`; the handler is not
called during that turn. One user message opens one response scope. Accepted
subagent work keeps it open, callback continuations remain in the same scope,
and completed, failed, or incomplete callbacks all settle one accepted
dispatch. Once every continuation turn has fully completed and no accepted
callback remains, the runtime first reconciles children touched by the scope:
unshared completed/failed children close, while incomplete children remain
open. It then calls each staged handler exactly once. Callback replays cannot
settle a newer follow-up dispatch.

The live response-scope event stream emits `PreEndScope` after the scope
becomes quiescent but before child cleanup and staged handlers. It emits
`EndScope` after cleanup, handler invocation, and scope removal. These are
scope-level events rather than per-turn `AgentEvent` values.

Framework tools (`load_skill` and root-only subagent tools) are owned by
`toolexecution`; application tools remain caller-owned. Model-facing
`close_subagent` requires `user_instruction`, the exact full text of the
current human turn. The manager rejects callback turns and shortened,
fabricated, or absent same-turn evidence.

Back to [tools-safety/index.md](index.md).
