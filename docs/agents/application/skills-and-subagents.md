# Skills and subagents

Skills live at `.agentcli/skill/{name}/SKILL.md`. Their name and description are
discovery metadata; full instructions load progressively through the
framework-owned `load_skill` tool. A model may load a skill after selecting it
by description, and any applicable instruction may explicitly require loading
one before the governed action or answer. Tool, subagent, and other capability
descriptions help selection but do not replace a required skill load. The
reload policy prevents needless recent duplication while refreshing
instructions after age, token, or content-change thresholds.

Subagents live at `.agentcli/agent/{name}/{name}.md` with validated name, description, provider, model, optional skills/tools, and Markdown instructions. Only the root Agent receives framework subagent tools; children cannot recursively spawn children.

The root subagent prompt separates catalog metadata, trigger selection,
instance addressing, dispatch-result handling, callbacks, lifecycle, and
safety. A direct description match may trigger delegation when the focused
work materially benefits from a configured child; an applicable instruction
or explicit user request may require delegation independently. Topic overlap
alone and discovery-only questions do not trigger a child.

Child permission and Yes/No confirmation questions bubble up to the parent
session. The manager converts retained child admission lifecycle facts into
parent-addressed events with parent, child, session, turn, call, and decision
identity. `SubscribeSubagentPermissions` and
`SubscribeSubagentConfirmations` are the live paths;
`PendingSubagentPermissions` and `PendingSubagentConfirmations` read durable
pending requests when attaching or reconnecting. The main-session UI answers
through `ResolveSubagentPermission` or `ResolveSubagentConfirmation`. This path
is independent from child-completion callbacks, which cannot fire while an
admission decision is unresolved.

Child sessions are always asynchronous. The model-facing `start_subagent` and
`send_subagent_message` tools produce `ContinueTurn`, which returns control to
the parent model but does not require assistant content or another tool call;
destructive close is application-owned and absent from the model catalog. An
accepted dispatch returns an acknowledgement with `callback_action=automatic`
plus the post-dispatch turn rule. The parent may continue only work planned
before dispatch that is outside the delegated task and independent of its
callback. Otherwise it ends the turn immediately without assistant content or
another tool call. It must not narrate waiting, call a response or delivery
tool, invent work, retry, redo delegated work, or poll. If a compatible parent
run is still active,
`TryInjectSubagentCallback` appends the trusted callback at the next provider
boundary; it never interrupts a live provider stream or tool handler.
Otherwise `ContinueSubagentCallbackSubscribed` starts a continuation turn.
Duplicate, already-sent, and callback-pending outcomes use
`callback_action=automatic_existing`; they create no new pending callback.
`selection_required` uses `callback_action=none` because nothing was
dispatched. A completed, incomplete, or failed outcome carries structured
summary/next-step fields, terminal error, and final assistant answer when
available. `report_subagent_outcome` is registered automatically only for
children; completed requires an explicit successful report. Its bounded repair
exposes only `report_subagent_outcome`, preventing repeated domain actions, and
defaults safely to incomplete if the child still omits a valid report.
Lifecycle (`running`, `idle`, `closed`) remains separate from last-turn
outcome.

Implicit `start_subagent` reuse is scoped to the requested definition: an open
child of another configured agent type is never a candidate. Start results
expose `accepted` and `deduplicated` with the same meaning as send results, so
callers count only accepted children in fan-out state. Created work is accepted;
selection, duplicate, already-sent, and callback-pending outcomes are not.
Ordinary lookup and research should start one child, evaluate its callback, and
start another only if needed. Multiple starts in one provider response are for
intentional independent comparison or parallel work.

The model-facing send path hashes normalized content with parent session, parent turn, and child ID. One parent turn may dispatch to a given child once: exact retries return `duplicate`, changed retries return `already_sent`, and neither reaches the mailbox; these idempotency decisions run before lifecycle admission, so a fast callback cannot turn a same-turn retry into an error. A new parent turn can send again. Running children accept FIFO mailbox input. Idle incomplete, completed, and failed children accept follow-up, new-task, and recovery input respectively only after the latest callback cursor was observed. When that callback is pending, model-facing send returns successful action `callback_pending` with `accepted=false`, `callback_action=automatic_existing`, and a short notice that the pending result will arrive automatically; direct Go/HTTP sends still return the lifecycle error. Closed and outcome-less children reject sends. The model has no list or status tools; the runtime supplies active instance summaries through `active_subagents`, and child results arrive only through callbacks. While a completion callback is pending, both routing tool results and the reminder omit last-turn error, outcome, summary, and next-step payloads.

One human root turn opens one response scope. Accepted child dispatches, inline
callback inputs, and callback continuation turns remain in that scope;
follow-up dispatches reopen its callback barrier. Inline reservations hold a
pending-input barrier until the callback is durably appended. The first-action
guard applies only to provider step one of the human root turn; a callback
continuation may deliver a final `EndResponseScope` tool on its first provider
round. Early root calls and calls while the scope is busy are successful
non-executing skips and are not retained. At the final boundary, the runtime
automatically closes touched idle children whose last outcome is completed or
failed, while retaining incomplete and cross-scope children.

Direct Go, Terminal, and HTTP close paths have the same application-owned destructive lifecycle and should be bound to explicit user actions. The manager enforces parent ownership, queues accepted child follow-ups, and preserves child transcripts and retained runs for UI views. Every successful explicit or automatic close emits `SystemSubagentClosed` on the live `SubscribeSystemEvents` stream; the HTTP session stream retains it as `subagent_closed`. See [subagent-lifecycle.md](subagent-lifecycle.md) for close ordering, cancellation markers, callback counters, and race behavior.

Back to [application/index.md](index.md).
