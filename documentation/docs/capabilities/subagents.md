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

When definitions exist, the root model receives six fixed framework tools:

| Tool | Use |
| --- | --- |
| `start_subagent` | Create or reuse a child and asynchronously route work. |
| `send_subagent_message` | Send focused follow-up work to a known child. |
| `close_subagent` | Release an idle child only after its callback has been consumed. It is not cancellation. Supports controlled turn completion for sequential cleanup. |
| `force_close_subagent` | Immediately interrupt and close a specific child only when the latest user message explicitly requests it. Drops queued child messages, retains history, and does not open a confirmation prompt. |
| `list_subagents` | List lightweight child summaries for explicit discovery or selection. |
| `subagent_status` | Read one compact snapshot for an explicit status question; repeated checks in one parent turn return the cached snapshot. |

Child agents do not receive these management tools. Every child instead
receives one framework-owned `report_subagent_outcome` tool. Before its final
answer, the child reports either `completed` or `incomplete` with a concise
summary and, for incomplete work, the required next step.

This outcome protocol is enforced by the child runtime, not only by prompt
wording. When a child tries to finish without a successful outcome report, the
runtime starts up to three bounded repair requests using the transcript that
was already stored. Each request exposes only `report_subagent_outcome`, so a
transfer, write, or other domain action that already ran cannot be invoked
again. The same restriction remains while the child writes its concise final answer.
There is no polling or second callback during repair.

If the repair reports `completed` or `incomplete`, that structured value is
authoritative. If the child still omits a valid report, the turn ends after the
bounded repair limit and emits an `incomplete` callback with a fallback summary.
A repair is never retried indefinitely.

## Asynchronous lifecycle

`start_subagent` and `send_subagent_message` return immediately after routing
work. They and `force_close_subagent` accept `finish_turn`, defaulting to
`true`. The model uses `false` only when it has already planned more
decomposition or operations after the current tool batch, and uses `true` on
the final dispatch, when none remain, or when unsure. `close_subagent` has no
`finish_turn` option and always continues to a normal provider round after
cleanup. The child turn outcome
arrives through a separate callback containing:

- parent and child identity;
- `completed`, `incomplete`, or `failed` status;
- structured summary and required next step when available;
- final assistant answer when one exists;
- terminal error when the child failed;
- durable transcript cursor metadata.

Each model-facing start, send, or force-close result echoes the resolved control state:

```json
{
  "finish_turn": false,
  "turn_behavior": "continue_turn",
  "instruction": "Continue only with additional planned dispatches..."
}
```

Final dispatches and force-close operations return `finish_turn: true` and
`turn_behavior: "end_turn"`. A `selection_required` result always reports
`false` and `continue_turn`. A normal close returns only
`turn_behavior: "continue_turn"` plus an instruction to deliver the callback.

When `start_subagent` returns `selection_required`, no work was routed, so that
turn continues only long enough for the parent to ask which `display_name` the
user means.

The callback is converted to trusted runtime input for a new parent turn. It is
not represented as a human message and is not attached as a late result to the
already completed `start_subagent` tool call.

```go
for callback := range agent.SubscribeSubagentCallbacks(ctx) {
    run, events, err := agent.ContinueSubagentCallbackSubscribed(ctx, callback)
    if errors.Is(err, agentruntime.ErrTurnInProgress) {
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

After delivering a bounded one-shot `completed` result, the parent should close
the child unless it has a concrete follow-up or explicit ongoing collaboration.
The possibility of a later question alone is not a reason to keep it open.

Sending follows lifecycle admission. A running child accepts FIFO mailbox
input. An idle `incomplete`, `completed`, or `failed` child accepts a focused
follow-up, distinct next task, or recovery instruction only after its latest
callback was consumed. For the model-facing tool, a pending callback is an
expected controlled result rather than a failed tool result:

```json
{
  "action": "callback_pending",
  "accepted": false,
  "finish_turn": true,
  "turn_behavior": "end_turn",
  "instruction": "No message was sent; wait for the authoritative callback."
}
```

The result preserves the requested `finish_turn`. `false` permits only concrete
operations already planned for other children; it never permits retrying the
same child or answering the user on the child's behalf. Tool calls already in
the same parallel batch still all run before AgentRuntime evaluates the batch.
Direct Go and HTTP sends continue returning the callback-pending lifecycle
error, while closed and outcome-less children remain rejected.

Closing is lifecycle cleanup, not cancellation. `CloseSubagent`, the model
tool, Terminal UI, and HTTP `DELETE` require a `completed` or `failed` outcome
whose latest callback cursor has been consumed. They reject running,
incomplete, and callback-pending children with a storage lifecycle error /
HTTP `409 conflict`. The model-facing tool converts these expected lifecycle
conflicts into a successful controlled result with `closed: false` and an
instruction not to retry, preventing a provider loop; direct Go and HTTP
callers retain the lifecycle error. This prevents a fast child from being
closed in the same parent turn that started it and prevents cleanup from
suppressing an unread callback.

When a parent closes completed work during its callback turn,
`close_subagent` always keeps the turn open:

```text
close → normal provider continuation → deliver callback → finish
```

This is the ordinary tool-result continuation path, not a completion repair or
retry. If content was already streamed alongside the close call, the parent
must not repeat it.

Context reminders are stable within one parent turn. If a child finishes
between provider rounds, the active turn continues seeing its original
snapshot; the queued callback becomes authoritative input in the following
callback turn.

`force_close_subagent` is the destructive escape hatch. Unlike normal close,
it can stop a running child or discard an incomplete child without waiting for
callback consumption. The tool is intentionally not protected by the generic
Yes/No confirmation mechanism. Its description and root orchestration prompt
restrict it to a specific child named by the latest explicit user request; the
model must never select it autonomously or use an older instruction as ongoing
authorization. Existing transcript messages and retained run events remain
available, while pending mailbox messages are removed and future sends are
rejected.

## Capacity

```go
agentcli.WithMaxSubagents(4)
```

The bound applies to non-closed children per parent session. Replace the default
relationship storage with `WithSubagentStorage` when child metadata must be
durable.
