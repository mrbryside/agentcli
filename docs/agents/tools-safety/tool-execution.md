# Tool execution

`task` is the one main-agent child-work framework tool. New calls use
agent/description/prompt; resumes use task_id/prompt. Presence of task_id is
authoritative: create-only agent/description fields are discarded before
validation and execution, so they cannot retarget a retained task. Same-batch
independent calls may run in parallel. Foreground is default, with
`background:true` and `WithTaskForegroundWait` promotion using Agent-owned exact-once delivery.
Continuation is an available protocol mode, not a framework policy requiring
reuse; application instructions decide when the same work should continue.
Children do not receive `task` or `EndResponseScope` tools. Step-limit
finalization is text-only and yields `incomplete`, with no report-repair tool.

`toolexecution.Tool` combines a provider-neutral definition, raw JSON handler,
one optional trigger mode, optional permission or policy-aware permission
descriptor, and optional confirmation descriptor. `Registry.Register` requires
a unique name, handler, supported trigger, and object-shaped schema.
Application tools may also configure either `ToolCallGuard` or
`ToolCallGuardPrompt`. Prompt tools optionally select one provider/model with
`*GuardModelConfig`; see [guardrails.md](guardrails.md) for evaluation and
retry behavior.

`RequiredSkills` is an optional hard prerequisite for custom tools. Every
listed skill must be available to the current agent and have a successful
`load_skill` result in the same runtime turn. Full instruction results and
`instructions_in_context=true` both satisfy the gate. Missing skills produce a
successful non-executing `reason=required_skill_not_loaded` result before
response-scope budgets, admission, guards, or handlers. The registry appends
the requirement to the cloned provider-facing description.

The root facade exposes `agentcli.Tool`, `ToolDefinition`, `InputSchema`,
permission/confirmation aliases, and trigger modes. `ObjectSchema` builds a
closed schema from a struct of `ToolParameter` descriptors; helpers cover all
JSON scalar types, objects, arrays, descriptions, required fields, and common
constraints. `RawInputSchema` is the validated escape hatch.
`DecodeArguments` strictly decodes one JSON object, rejects unknown fields, and
rejects trailing values. There is no typed custom-tool inference wrapper.

`ResponseScopeCallLimit` sets a hard cumulative call budget for one tool across
the main-agent turn and every inline/result continuation in the same response
scope. Exhaustion returns successful `status=skipped` with
`reason=response_scope_tool_budget_exhausted` without running admission, the
handler, or network I/O. Admitted attempts count even if their handler later
fails, so retries cannot bypass the limit.

`Executor.Run` applies admission, assignments through a bounded worker pool,
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
triggers. `task` is the only main-agent child-work framework tool. A foreground
call returns final output in place; background or foreground-wait promotion is
delivered exactly once by Agent. Independent calls in one batch use the bounded
worker pool, and task is absent from child registries. Destructive subagent
close is application-owned and is not registered in the model tool catalog.

`EndTurn` executes its handler immediately. Its latest successful result
anywhere in the turn satisfies the requirement; a later failed attempt makes it
missing again. `EndTurnOnSuccess` independently decides whether every
successful invocation should end its batch. A handler may instead call
`RequestEndTurn(ctx)` to make that choice for one invocation. The request takes
effect only after the handler and every result in the same batch succeed;
handler failure or interruption discards it. If the model attempts to finish
while a trigger tool is missing, repair rounds expose only the missing trigger
tools and add a reminder naming each one. If a caller-supplied completion guard
also requests a bounded tool allowlist, its tools are merged
with the missing trigger tools.
There are at most three consecutive no-progress repairs; progress resets the
budget. Exhaustion fails the turn.

For root user-visible delivery, prefer `Trigger: EndResponseScope`; add
`EndTurnOnSuccess: true` when successful final delivery should finish the
current turn. Subagents do not support `EndResponseScope`; at their provider
step limit, no tools are exposed and one text-only final answer yields
`incomplete`. Successful root delivery remains represented by its
durable tool call and tool result; the runtime does not synthesize an assistant
message from tool arguments. A call made as the initial human main-agent turn's first provider action,
or while the response scope is still busy,
receives `status=succeeded`, `action=skipped`, `executed=false`, and
`reason=tool_called_at_wrong_time`. It does not satisfy the trigger. The result
explicitly tells the model to treat the call as success and not retry it. The
injected tool description directs the model to continue the remaining work
until completion repair requests the final call. Admission and the handler are
bypassed, and no candidate is retained. `EndTurnOnSuccess` is still honored,
allowing an early final-delivery
call to yield the current turn while results are pending without executing
the handler. If the scope is otherwise quiescent, the premature call continues
the current turn instead, preserving access to ordinary work tools. This
includes a call made as the human main-agent model's first tool. A result
continuation may execute the final tool on provider step one because it is not
the initial human action. For compatibility, the coordinator can execute a
later direct root call when it is the last active scope turn and no result or
pending runtime input remains, but the model-facing contract does not instruct
the model to retry that way. A normal completion attempt makes completion
repair expose the missing `EndResponseScope` tools with a final-call reminder.
One user message opens one response scope. Accepted
subagent work keeps it open, and intermediate main agent/result assistant drafts
are discarded until the final scope turn.

The live response-scope event stream emits `PreEndScope` after the scope
enters its final completion boundary but before subagent cleanup and final
handlers. It emits
`EndScope` after cleanup, handler invocation, and scope removal. These are
scope-level events rather than per-turn `AgentEvent` values.

Framework tools (`load_skill` and root-only subagent tools) are owned by
`toolexecution`; application tools remain caller-owned. Go, Terminal, and HTTP
close paths call the same manager operation. After durable close, it cancels
every outstanding unreserved assignment for that subagent and decrements the owning
response scopes' result barriers.

Back to [tools-safety/index.md](index.md).
