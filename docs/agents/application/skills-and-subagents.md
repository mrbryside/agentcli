# Skills and subagents

Skills live at `.agentcli/skill/{name}/SKILL.md`. Their name and description are
discovery metadata; full instructions load progressively through the
framework-owned `load_skill` tool. A model may load a skill after selecting it
by description, and any applicable instruction may explicitly require loading
one before the governed action or answer. Tool, subagent, and other capability
descriptions help selection but do not replace a required skill load. Whenever
a load trigger applies, the model calls `load_skill` instead of inferring that
a visible historical body is still current. The runtime reload policy returns
`already_loaded` to prevent needless recent duplication while refreshing
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

Child sessions are always asynchronous. `start_subagent` requires
`continue_after_dispatch`. False requests a turn end through the same
handler-controlled mechanism available to custom tools; it takes effect only
when the call returns a pending callback and the complete tool batch succeeds.
True returns control to the parent model for already-planned work outside the
delegated task that is independent of the callback. Every parallel start in one
batch must use the same value: all false to wait, or all true to continue.
`send_subagent_message` returns control and uses the passive post-dispatch
policy. Destructive close is application-owned and absent from the model
catalog. An accepted dispatch returns an acknowledgement with
`callback_action=automatic`. The parent must not narrate waiting, call a
response or delivery tool, invent work, retry, redo delegated work, or poll. If
a compatible parent run is still active,
`TryInjectSubagentCallback` appends the trusted callback at the next provider
boundary; it never interrupts a live provider stream or tool handler.
Otherwise `ContinueSubagentCallbackSubscribed` starts a continuation turn.
Duplicate, already-sent, and callback-pending outcomes use
`callback_action=automatic_existing`; they create no new pending callback.
Every successful `start_subagent` call creates a new separately addressed
child; it never reuses or continues an existing child. A completed, incomplete,
or failed outcome carries structured summary/next-step fields, terminal error,
and final assistant answer when available. `completed` forbids `next_step` and
`error`; `incomplete` requires one concrete `next_step`; `failed` requires the
actual terminal `error` and forbids `next_step`.
`report_subagent_outcome` is registered automatically only for children; every
semantic outcome requires an explicit successful report.
Its bounded repair
exposes only `report_subagent_outcome`, preventing repeated domain actions, and
defaults safely to incomplete if the child still omits a valid report.
Lifecycle (`running`, `idle`, `closed`) remains separate from last-turn
outcome.

Start results are always accepted when creation succeeds. Ordinary lookup and
research should start one child, evaluate its callback, and start another only
if needed. Multiple starts in one provider response are for intentional
independent comparison or parallel work. Follow-ups target a known idle
incomplete or failed child through `send_subagent_message` after its latest
callback is consumed. Completed children are not reused.

The model-facing send path hashes normalized content with parent session, parent
turn, and child ID. One parent turn may dispatch to a given child once: exact
retries return `duplicate`, changed retries return `already_sent`, and neither
reaches the mailbox; these idempotency decisions run before lifecycle
admission, so a fast callback cannot turn a same-turn retry into an error. A new
parent turn can send again. Model-facing sends target idle children only after
the latest callback cursor was observed. A running child returns
`callback_pending` with `accepted=false`,
`callback_action=automatic_existing`, and a short notice that the pending
result will arrive automatically. An observed completed child returns
`child_completed` without dispatch and is not reused. Direct Go/HTTP sends may
still queue FIFO input behind a running child, but completed, closed, and
outcome-less children reject sends.
The model has no list or status tools; the runtime supplies active instance
summaries through `active_subagents`, and child results arrive only through
callbacks. While a completion callback is pending, both routing tool results
and the reminder omit last-turn error, outcome, summary, and next-step payloads.

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

Every trusted callback runtime message includes an atomic `callback_progress`
snapshot for its response scope. `pending_callbacks` identifies outstanding
accepted dispatches by child identity and dispatch ID, with a child turn ID
when one already exists. `received_callbacks` identifies delivered child turns
and their outcome status. Reservation rollback removes the unaccepted received
entry and restores its pending dispatch.

The provider-facing `report_subagent_outcome` schema is intentionally a flat
object using `properties`, `required`, and a status `enum`. Status-dependent
rules remain authoritative in the runtime parser: it rejects forbidden fields
and requires `next_step` or `error` when appropriate. Keeping conditional
validation out of root-level JSON Schema combinators avoids compatible
providers that expose the tool but fail to generate arguments for nested
`oneOf` branches.

Direct Go, Terminal, and HTTP close paths have the same application-owned destructive lifecycle and should be bound to explicit user actions. The manager enforces parent ownership, queues accepted child follow-ups, and preserves child transcripts and retained runs for UI views. Every successful explicit or automatic close emits `SystemSubagentClosed` on the live `SubscribeSystemEvents` stream; the HTTP session stream retains it as `subagent_closed`. See [subagent-lifecycle.md](subagent-lifecycle.md) for close ordering, cancellation markers, callback counters, and race behavior.

Back to [application/index.md](index.md).
