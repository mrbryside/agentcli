# Skills and subagents

Skills live at `.agentcli/skill/{name}/SKILL.md`. Their name and description are discovery metadata; full instructions load progressively through the framework-owned `load_skill` tool. The reload policy prevents needless recent duplication while refreshing instructions after age, token, or content-change thresholds.

Subagents live at `.agentcli/agent/{name}/{name}.md` with validated name, description, provider, model, optional skills/tools, and Markdown instructions. Only the root Agent receives framework subagent tools; children cannot recursively spawn children.

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

Child sessions are always asynchronous. The model-facing `start_subagent`, `send_subagent_message`, and user-directed `close_subagent` tools always produce `ContinueTurn`. The parent issues exactly one start call per provider round and never batches multiple starts in one tool-call response. After an accepted start or send, its result exposes `callback_action=wait`, `must_wait_for_callback=true`, and prohibited actions. The parent may continue already-planned independent work that neither duplicates the delegated task nor depends on its result. It must not retry the dispatch, redo delegated work, poll, inspect status to wait, or claim completion before the callback. Once independent work is exhausted, the parent completes through the application's normal response or required trigger tool so the callback can arrive on a later turn. Duplicate, already-sent, and callback-pending outcomes use `callback_action=wait_existing`; they create no new pending callback. `selection_required` uses `callback_action=none` because nothing was dispatched. A completed, incomplete, or failed outcome later arrives through a compact callback containing structured summary/next-step fields, terminal error, and final assistant answer when available. `report_subagent_outcome` is registered automatically only for children; completed requires an explicit successful report. If a child attempts to finish without one, its runtime performs up to three bounded repair requests against the already-persisted transcript, exposes only `report_subagent_outcome`, and uses a reminder instructing the child to report the outcome without repeating domain work. If all repairs omit a valid report, the callback safely defaults to incomplete. Lifecycle (`running`, `idle`, `closed`) remains separate from the last-turn outcome.

Implicit `start_subagent` reuse is scoped to the requested definition: an open
child of another configured agent type is never a candidate. Start results
expose `accepted` and `deduplicated` with the same meaning as send results, so
callers count only accepted children in fan-out state. Created work is accepted;
selection, duplicate, already-sent, and callback-pending outcomes are not.

The model-facing send path hashes normalized content with parent session, parent turn, and child ID. One parent turn may dispatch to a given child once: exact retries return `duplicate`, changed retries return `already_sent`, and neither reaches the mailbox; these idempotency decisions run before lifecycle admission, so a fast callback cannot turn a same-turn retry into an error. A new parent turn can send again. Running children accept FIFO mailbox input. Idle incomplete, completed, and failed children accept follow-up, new-task, and recovery input respectively only after the latest callback cursor was observed. When that callback is pending, model-facing send returns successful action `callback_pending` with `accepted=false`, `callback_action=wait_existing`, and an instruction not to retry or answer for the child; direct Go/HTTP sends still return the lifecycle error. Closed and outcome-less children reject sends. Parent context reminders are snapshotted on their first provider round and remain stable for the whole parent turn, so a callback completing mid-turn cannot leak partial lifecycle state into later rounds; the next turn sees the durable update. Avoid polling: list is for explicit discovery/selection, while status returns one fresh snapshot per child/parent-turn and caches repeats as `already_checked`.

One human root turn opens one response scope. Accepted child dispatches and their callback continuations remain in that scope; follow-up dispatches reopen its callback barrier. At quiescence, before `EndResponseScope` handlers, the runtime automatically closes touched idle children whose last outcome is completed or failed. It retains incomplete children and any child referenced by another live scope. Successful automatic closes become one trusted, ephemeral reminder on the next accepted human turn; callback turns do not consume it and it is never persisted as user content.

`close_subagent` is no longer routine cleanup. It destructively interrupts and closes one child only when the current human message explicitly requests that action. Its required `user_instruction` must reproduce the exact full same-turn user message; callback turns and shortened, fabricated, or older evidence are rejected. Direct Go, Terminal, and HTTP close paths have the same destructive lifecycle effect and should be bound to explicit user actions. The manager enforces parent ownership, queues accepted child follow-ups, and preserves child transcripts and retained runs for UI views. Every successful explicit or automatic close also emits `SystemSubagentClosed` on the live `SubscribeSystemEvents` stream; the HTTP session stream retains it as `subagent_closed`.

Back to [application/index.md](index.md).
