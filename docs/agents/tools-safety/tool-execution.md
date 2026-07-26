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

`ResponseScopeCallLimit` sets a hard cumulative call budget for one tool across
the root turn and every inline/callback continuation in the same response
scope. Exhaustion returns successful `status=skipped` with
`reason=response_scope_tool_budget_exhausted` without running admission, the
handler, or network I/O. Admitted attempts count even if their handler later
fails, so retries cannot bypass the limit.

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
Neither trigger ends the turn by itself. `EndTurnOnSuccess: true` is separate
from `Trigger` and ends the turn when every result in that tool batch succeeds.
During registration, the registry appends runtime-owned guidance to the cloned
tool description for each configured trigger and for `EndTurnOnSuccess`. The
provider therefore sees when to call the tool, whether the handler runs
immediately or only at the final scope boundary, the exact early-skip result,
and whether a successful batch ends the turn. The caller's original
`ToolDefinition` is not mutated.
Without a trigger the tool is optional and immediate; with a trigger it keeps
that trigger's requirement and delivery timing. One such tool can end a mixed
batch containing normal immediate tools, but it cannot bypass missing required
triggers. Framework `start_subagent` and `send_subagent_message` always
continue. Their accepted result is intentionally only `Accepted. The result
will arrive automatically later.` The callback joins a compatible active
parent at its next provider boundary or falls back to a continuation turn.
Destructive child close is application-owned and is not registered in the
model tool catalog.

`EndTurn` executes its handler immediately. Its latest successful result
anywhere in the turn satisfies the requirement; a later failed attempt makes it
missing again. `EndTurnOnSuccess` independently decides whether the successful
batch ends the turn. If the model attempts to finish while a trigger tool is
missing, repair rounds expose only the missing
trigger tools and add a reminder naming each one. If a caller-supplied
completion guard also requests a bounded tool allowlist, its tools are merged
with the missing trigger tools.
There are at most three consecutive no-progress repairs; progress resets the
budget. Exhaustion fails the turn.

For user-visible delivery, prefer `Trigger: EndResponseScope`; add
`EndTurnOnSuccess: true` when successful final delivery should finish the
current turn. Set `CanonicalAssistantMessageParameter` to a required string
argument such as `message` when successful external delivery should append
that exact value as the durable assistant response. The canonical record is
created only after the handler succeeds. A call made as the initial human root
turn's first provider action, or while the response scope is still busy,
receives successful `status=skipped`,
`executed=false` and does not satisfy the trigger. The result tells the model
not to retry in that provider round; admission and the handler are bypassed, and no candidate is
retained. `EndTurnOnSuccess` is still honored, allowing an early final-delivery
call to yield the current turn while callbacks are pending without executing
the handler. If the scope is otherwise quiescent, the premature call continues
the current turn instead, preserving access to ordinary work tools. This
includes a call made as the human root model's first tool. A callback
continuation may execute the final tool on provider step one because it is not
the initial human action. A later root call can execute directly when it is the
last active scope turn and no callback or pending runtime input remains. A normal completion attempt can
also make completion repair expose the missing `EndResponseScope` tools with a
final-call reminder. One user message opens one response scope. Accepted
subagent work keeps it open, and intermediate parent/callback assistant drafts
are discarded until the final scope turn.

The live response-scope event stream emits `PreEndScope` after the scope
enters its final completion boundary but before child cleanup and final
handlers. It emits
`EndScope` after cleanup, handler invocation, and scope removal. These are
scope-level events rather than per-turn `AgentEvent` values.

Framework tools (`load_skill` and root-only subagent tools) are owned by
`toolexecution`; application tools remain caller-owned. Go, Terminal, and HTTP
close paths call the same manager operation. After durable close, it cancels
every outstanding unreserved dispatch for that child and decrements the owning
response scopes' callback barriers.

Back to [tools-safety/index.md](index.md).
