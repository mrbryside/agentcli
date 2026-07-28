# Skills and subagents

Skills live at `.agentcli/skill/{name}/SKILL.md`. Their name and description are
discovery metadata; full instructions load progressively through the
framework-owned `load_skill` tool. A model may load a skill after selecting it
by description, and any applicable instruction may explicitly require loading
one before the governed action or answer. Tool, subagent, and other capability
descriptions help selection but do not replace a required skill load. Whenever
a load trigger first applies in a turn, the model calls `load_skill` instead of
inferring that a visible historical body is still current. A successful load
from an earlier turn does not satisfy a new load trigger. Each call loads only
the exact skill named in the request; skills are never loaded collectively.
Every successful result uses `status=loaded`, and its `name` applies only to
that named skill. A full load also includes `description` and `instructions`;
a cached load instead returns `instructions_in_context=true`. Both forms
include a plain `message` explaining where the instructions are. A tool result
and later provider steps continue the current turn and do not create another
load trigger by themselves. Each named skill may be loaded at most once per runtime
turn. After it loads, the model must not load that skill again until the next
`<turn_start>` marker. Another skill still requires
its own separate valid trigger and load. A lightweight result with
`instructions_in_context=true` means the named skill's full body is already
available in the conversation context and was not repeated. The loader only
makes that named skill's instructions available; those instructions and
current context determine whether the turn continues, waits, or ends. The
runtime reload policy refreshes instructions after age, token, or
content-change thresholds.

The framework injects a trusted `<turn_start>` system reminder only
on the first provider request of each runtime turn. Later provider steps in
that turn do not receive the marker. The reminder identifies the new turn and
resets the model's turn-scoped loaded-skill set. Later provider steps or tool
results never reset that set. The reminder is ephemeral and is never stored in
the conversation transcript.

Subagents live at `.agentcli/agent/{name}/{name}.md` with validated name, description, provider, model, optional skills/tools, and Markdown instructions. Only the main agent receives framework subagent tools; subagents cannot recursively spawn subagents.

The main-agent subagent prompt separates catalog metadata, trigger selection,
instance addressing, assignment-result handling, results, lifecycle, and
safety. A direct description match may trigger delegation when the focused
work materially benefits from a configured subagent; an applicable instruction
or explicit user request may require delegation independently. Topic overlap
alone and discovery-only questions do not trigger a subagent.

Subagent permission and Yes/No confirmation questions bubble up to the main agent
session. The manager converts retained subagent admission lifecycle facts into
main agent-addressed events with main agent, subagent, session, turn, call, and decision
identity. `SubscribeSubagentPermissions` and
`SubscribeSubagentConfirmations` are the live paths;
`PendingSubagentPermissions` and `PendingSubagentConfirmations` read durable
pending requests when attaching or reconnecting. The main-session UI answers
through `ResolveSubagentPermission` or `ResolveSubagentConfirmation`. This path
is independent from subagent-completion results, which cannot fire while an
admission decision is unresolved.

Subagent sessions are always asynchronous. `start_subagent` requires
`continue_main_agent`. False requests a turn end through the same
handler-controlled mechanism available to custom tools; it takes effect only
when the call returns a pending result and the complete tool batch succeeds.
True returns control to the main agent model for already-planned work outside the
delegated task that is independent of the result. Every parallel start in one
batch must use the same value: all false to wait, or all true to continue.
`send_subagent_message` also requires `continue_main_agent`: false ends an
accepted successful batch to wait, while true returns control only for
already-planned independent work. Duplicate, already-sent, and result-pending
results end the successful batch regardless of that value. Destructive close is
application-owned and absent from the model catalog. An accepted assignment
returns `accepted`, `action`, `result_delivery`, `main_agent_action`, and
`instruction`; `result_delivery=automatic` means the result will arrive later.
The main agent must not narrate waiting, call a
response or delivery tool, invent work, retry, redo delegated work, or poll. If
a compatible main agent run is still active,
`TryInjectSubagentResult` appends the trusted result at the next provider
boundary; it never interrupts a live provider stream or tool handler.
Otherwise `ContinueSubagentResultSubscribed` starts a continuation turn.
Duplicate, already-sent, and result-pending calls use
`result_delivery=existing`; they create no new pending result.
`recovery_exhausted` returns `result_delivery=none` and
`main_agent_action=report_terminal_failure` so the main agent can report
the terminal failure; the same normalized failure receives at most one recovery
assignment per subagent in one response scope.
Every successful `start_subagent` call creates a new separately addressed
subagent; it never reuses or continues an existing subagent. A completed, incomplete,
or failed result carries structured summary/next-step fields, terminal error,
and final assistant answer when available. `completed` forbids `next_step` and
`error`; `incomplete` requires one concrete `next_step`; `failed` requires the
actual terminal `error` and forbids `next_step`.
`report_subagent_result` is registered automatically only for subagents; every
structured result requires an explicit successful report.
Its bounded repair
exposes only `report_subagent_result`, preventing repeated domain actions, and
defaults safely to incomplete if the subagent still omits a valid report.
When a configured provider-step limit is exhausted, the initial subagent
finalizer exposes `report_subagent_result` immediately; the usual maximum of
three result-report repairs remains unchanged, and one final text round remains
available after a report succeeds on the last repair. Application
`EndResponseScope` tools are main-agent-only and are rejected when assigned to a
subagent, both during main-agent project validation and defensively during direct
subagent construction.
Lifecycle (`running`, `idle`, `closed`) remains separate from last-turn
result status.

Start results are always accepted when creation succeeds. Ordinary lookup and
research should start one subagent, evaluate its result, and start another only
if needed. Multiple starts in one provider response are for intentional
independent comparison or parallel work. Follow-ups target a known idle
incomplete or failed subagent through `send_subagent_message` after its latest
result is consumed. Completed subagents are not reused.

The model-facing send path hashes normalized content with main agent session, main agent
turn, and subagent ID. One main-agent turn may assign to a given subagent once: exact
retries return `duplicate`, changed retries return `already_sent`, and neither
reaches the mailbox; these idempotency decisions run before lifecycle
admission, so a fast result cannot turn a same-turn retry into an error. A new
main agent turn can send again. Model-facing sends target idle subagents only after
the latest result cursor was observed. A running subagent returns
`result_pending` with `accepted=false`,
`result_delivery=existing`, `main_agent_action=stop_and_wait`, and a short
notice that the pending result will arrive automatically. An observed completed subagent returns
`subagent_completed` without assignment and is not reused. Direct Go/HTTP sends may
still queue FIFO input behind a running subagent, but completed, closed, and
subagents without a usable result reject sends.
The model has no list or status tools; the runtime supplies active instance
summaries through `active_subagents`, and subagent results arrive only through
results. While a completion result is pending, both routing tool results
and the reminder omit result error, status, summary, and next-step payloads.

One human main-agent turn opens one response scope. Accepted subagent assignments, inline
result inputs, and result continuation turns remain in that scope;
follow-up assignments reopen its result barrier. Inline reservations hold a
pending-input barrier until the result is durably appended. The first-action
guard applies only to provider step one of the human main-agent turn; a result
continuation may deliver a final `EndResponseScope` tool on its first provider
round. Early main-agent calls and calls while the scope is busy are successful
non-executing skips and are not retained. At the final boundary, the runtime
automatically closes touched idle subagents whose last result status is completed or
failed, while retaining incomplete and cross-scope subagents.

Every trusted result runtime message includes an atomic `result_progress`
snapshot for its response scope. `pending_results` identifies outstanding
accepted assignments by subagent identity and `assignment_id`, with
`subagent_turn_id` when one already exists. `delivered_results` identifies
delivered subagent turns with `subagent_turn_id` and `result_status`.
Reservation rollback removes the unaccepted received
entry and restores its pending assignment.

The provider-facing `report_subagent_result` schema is intentionally a flat
object using `properties`, `required`, and a status `enum`. Status-dependent
rules remain authoritative in the runtime parser: it rejects forbidden fields
and requires `next_step` or `error` when appropriate. Keeping conditional
validation out of root-level JSON Schema combinators avoids compatible
providers that expose the tool but fail to generate arguments for nested
`oneOf` branches.

Direct Go, Terminal, and HTTP close paths have the same application-owned destructive lifecycle and should be bound to explicit user actions. The manager enforces main agent ownership, queues accepted subagent follow-ups, and preserves subagent transcripts and retained runs for UI views. Every successful explicit or automatic close emits `SystemSubagentClosed` on the live `SubscribeSystemEvents` stream; the HTTP session stream retains it as `subagent_closed`. See [subagent-lifecycle.md](subagent-lifecycle.md) for close ordering, cancellation markers, result counters, and race behavior.

Back to [application/index.md](index.md).
