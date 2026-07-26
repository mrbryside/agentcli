---
title: Subagents
sidebar_position: 2
---

# Subagents

A subagent is a child session with its own model, instructions, transcript,
tool/skill allowlists, streaming run, and friendly random display name. The
root agent is the only agent allowed to manage children; nesting stops at one
level.

## Definition

Create `.agentcli/agent/<directory>/<name>.md`:

```markdown
---
name: researcher
description: Use for substantial research requiring project inspection, evidence, or trade-off comparison.
provider: openai
model: gpt-4.1-mini
skills:
  - source-review
tools:
  - glob
  - read
---

Investigate the delegated question. Return verified facts, uncertainties,
trade-offs, and a concise recommendation to the parent.
```

`name`, `description`, `provider`, and `model` are required. Omit `skills` or
`tools` when none are allowed. Every name and provider is validated during
project loading/agent initialization.

## Root management tools

When definitions exist, the root model receives four fixed framework tools:

| Tool | Use |
| --- | --- |
| `start_subagent` | Create or reuse a child and asynchronously route work. |
| `send_subagent_message` | Send focused follow-up work to a known child. |
| `list_subagents` | List lightweight child summaries for explicit discovery or selection. |
| `subagent_status` | Read one compact snapshot for an explicit status question; repeated checks in one parent turn return the cached snapshot. |

Child agents do not receive these management tools. Every child instead
receives one framework-owned `report_subagent_outcome` tool. Before its final
answer, the child reports either `completed` or `incomplete` with a concise
summary and, for incomplete work, the required next step.
Destructive closure is not model-facing. Applications may close a child through
the Go API, Terminal, or HTTP endpoint. See
[Subagent lifecycle control](./subagent-lifecycle-control.md) for the ownership
contract and response-scope accounting.

This outcome protocol is enforced by the child runtime, not only by prompt
wording. When a child tries to finish without a successful outcome report, the
runtime starts up to three bounded repair requests using the transcript that
was already stored. Each request exposes only `report_subagent_outcome` and
uses a reminder to call it, so the model cannot repeat a transfer, write, or
other domain action that already ran. The same instruction remains while the
child writes its concise final answer.
There is no polling or second callback during repair.

If the repair reports `completed` or `incomplete`, that structured value is
authoritative. If the child still omits a valid report, the turn ends after the
bounded repair limit and emits an `incomplete` callback with a fallback summary.
A repair is never retried indefinitely.

## Asynchronous lifecycle

`start_subagent` and `send_subagent_message` return immediately after routing
work and always continue the parent turn. The model issues
exactly one start per provider round and never batches multiple starts in one
tool-call response. Accepted start/send results
set `callback_action: wait` and `must_wait_for_callback: true`. While a child
runs, the parent may continue already-planned independent work that neither
duplicates the delegated task nor depends on its result. It must not retry the
dispatch, redo delegated work, poll, or claim completion before the callback.
Once independent work is exhausted, the model completes through the
application's normal response or required trigger tool so callbacks can arrive on
later turns. Duplicate, already-sent, and callback-pending results use
`callback_action: wait_existing` because they create no new callback.
`selection_required` uses `callback_action: none`. The child turn outcome
arrives through a separate callback containing:

- parent and child identity;
- `completed`, `incomplete`, or `failed` status;
- structured summary and required next step when available;
- final assistant answer when one exists;
- terminal error when the child failed;
- durable transcript cursor metadata.

Each model-facing start result reports that the parent remains open:

```json
{
  "accepted": true,
  "callback_action": "wait",
  "must_wait_for_callback": true,
  "prohibited_actions": [
    "duplicate_delegated_work",
    "work_depending_on_callback_result",
    "poll",
    "retry_same_dispatch",
    "claim_completion_before_callback"
  ],
  "turn_behavior": "continue_turn",
  "next_action": "Continue independent non-duplicative work, then finish the parent turn and wait for the authoritative callback."
}
```

Send results also report `turn_behavior: "continue_turn"`. A
`selection_required` start result reports
`callback_action: "none"` and `turn_behavior: "continue_turn"`.

When `start_subagent` returns `selection_required`, no work was routed, so that
turn continues only long enough for the parent to ask which `display_name` the
user means.

The callback is converted to trusted runtime input for a new parent turn. It is
not represented as a human message and is not attached as a late result to the
already completed `start_subagent` tool call.

```go
for callback := range agent.SubscribeSubagentCallbacks(ctx) {
    run, events, err := agent.ContinueSubagentCallbackSubscribed(ctx, callback)
    if errors.Is(err, agentcli.ErrTurnInProgress) {
        // Keep the callback queued and retry when the parent session is free.
        continue
    }
    if err != nil {
        log.Printf("callback: %v", err)
        continue
    }
    for event := range events.Events {
        render(event)
    }
    _, _ = run.Result()
}
```

Failures also produce callbacks, so a failed child cannot leave the parent
waiting forever without information.

Lifecycle and outcome are intentionally separate. `running`, `idle`, and
`closed` describe whether the child process can accept work. Callback outcome
describes whether the delegated task is actually resolved:

| Outcome | Meaning |
| --- | --- |
| `completed` | The child explicitly reported that all required delegated work is resolved. |
| `incomplete` | The turn ended normally, but work, information, or a decision remains. A report still missing after the bounded repair requests defaults here. |
| `failed` | The provider/runtime turn ended with an error. |

The runtime never infers completion merely because the provider stopped
without an error.

## Reuse behavior

Starting work with no matching open child creates one. When exactly one child
is open, conversational follow-ups reuse it. When several could match, the tool
returns `selection_required` and the parent asks the user which friendly name
to continue with. A new child is forced only when the user explicitly requests
new, separate, another, or parallel work.

## Queued follow-ups

`SendSubagentMessage` starts immediately when the child is idle or queues behind
its current turn. The next callback is produced for each completed queued turn.
The parent should use callbacks rather than repeated status/read polling.

`subagent_status` permits one fresh snapshot per child and parent turn. A repeat
returns `action: already_checked` with the original cached snapshot, so it
cannot be used to discover completion sooner. `list_subagents` remains
available for explicit discovery and selecting among multiple open children;
it is not a progress API.

The model-facing `send_subagent_message` tool enforces one accepted message per
`(parent session, parent turn, child)` tuple. Its internal SHA-256 idempotency
key also includes normalized message content:

```text
SHA-256(parentSessionID + parentTurnID + subagentID + normalizedMessage)
```

An exact retry returns `action: duplicate`; a different second message from
the same parent turn returns `action: already_sent`. Neither invocation starts
a child turn or adds mailbox work. This check happens before lifecycle
admission, so even when a fast child has already produced a callback, a
same-parent-turn retry remains `duplicate` or `already_sent` instead of becoming
a tool error. A later parent turn may send again. Direct
application calls to `Agent.SendSubagentMessage` represent explicit UI/user
input and are not restricted by the model-facing parent-turn guard.

## Results and closing

Callbacks carry only the final assistant answer, not every tool call and
intermediate message. `ListMessages` remains available for a full child UI.
`ReadSubagent` is a recovery API that consumes the latest unobserved final
answer; it is not exposed as a model tool.

Sending follows lifecycle admission. A running child accepts FIFO mailbox
input. An idle `incomplete`, `completed`, or `failed` child accepts a focused
follow-up, distinct next task, or recovery instruction only after its latest
callback was consumed. For the model-facing tool, a pending callback is an
expected controlled result rather than a failed tool result:

```json
{
  "action": "callback_pending",
  "accepted": false,
  "callback_action": "wait_existing",
  "must_wait_for_callback": true,
  "turn_behavior": "continue_turn",
  "instruction": "No message was sent; continue only independent non-duplicative work, then finish the parent turn and wait for the pending authoritative callback."
}
```

The result creates no new callback. It permits already-planned independent work
that neither duplicates the delegated task nor depends on the callback, but
never permits retrying the same child or answering on the child's behalf. Tool
calls already in the same parallel batch still all run before AgentRuntime
evaluates the batch. Direct Go and HTTP sends continue returning the
callback-pending lifecycle error, while closed and outcome-less children remain
rejected.

Routine lifecycle cleanup happens at the response-scope boundary. One human
user message opens one response scope. Accepted child dispatches keep that
scope open; every callback continuation remains in the same scope, and a
follow-up accepted from that continuation adds another pending callback. The
final boundary is available only when one last active turn is attempting
completion and no accepted callback obligation remains. Earlier
`EndResponseScope` calls are successful non-executing skips and are not retained
for later delivery.

Immediately before final `EndResponseScope` handlers execute, the runtime
reconciles every child that accepted work in that scope:

- idle `completed` and `failed` children are automatically closed;
- `incomplete` children remain open for a later focused follow-up;
- running, queued, callback-pending, or otherwise unsettled children prevent
  the scope from becoming quiescent;
- a child referenced by another live response scope is retained so one scope
  cannot close work owned by another.

Automatic close retains the transcript and run-event history. Successful
closures are grouped into a trusted one-shot context reminder on the next
accepted human user turn. Callback turns do not consume this reminder, it is
not persisted as user content, and it tells the model that those children can
no longer receive follow-up messages.

Context reminders are stable within one parent turn. If a child finishes
between provider rounds, the active turn continues seeing its original
snapshot; the queued callback becomes authoritative input in the following
callback turn.

Application-owned close operations can stop a running child or discard an
incomplete child. They are available through `Agent.CloseSubagent`, Terminal
`/close`, and the HTTP `DELETE` endpoint, but not through the model tool
catalog. Existing transcript messages and retained run events remain available,
while pending mailbox messages are removed and future sends are rejected.
Closing also makes callback cancellation terminal for that child: queued
dispatches release their response-scope counters, racing registrations are
ignored, and a rejected callback reservation cannot restore an obligation.
This releases the callback barrier without creating a provider turn or
user-visible response. Bind these destructive surfaces only to explicit
application or user actions. The complete contract is documented in
[Subagent lifecycle control](./subagent-lifecycle-control.md).

## Capacity

Set the project-wide default in `.agentcli/config.yaml`:

```yaml
max_subagents: 4
```

This limits non-closed children per parent session. Omitting the field uses the
default of 4; `0` has the same meaning. `WithMaxSubagents` can override it
programmatically.

```go
agentcli.WithMaxSubagents(4)
```

The bound applies to non-closed children per parent session. Replace the default
relationship storage with `WithSubagentStorage` when child metadata must be
durable.
