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

When definitions exist, the root model receives two fixed framework tools:

| Tool | Use |
| --- | --- |
| `start_subagent` | Create a new child and asynchronously route one focused assignment. |
| `send_subagent_message` | Send focused follow-up work to a known child. |

Child agents do not receive these management tools. Every child instead
receives one framework-owned `report_subagent_outcome` tool. Before its final
answer, the child reports one semantic outcome with a concise summary:

- `completed` means all delegated work is resolved and forbids `next_step`;
- `incomplete` means required work or information remains and requires one
  concrete `next_step`;
- `failed` means a terminal error prevents continuation, requires the actual
  `error`, and forbids `next_step`.

The child never invents recovery work merely to populate an outcome field.
Destructive closure is not model-facing. Applications may close a child through
the Go API, Terminal, or HTTP endpoint. See
[Subagent lifecycle control](./subagent-lifecycle-control.md) for the ownership
contract and response-scope accounting.

The model-facing outcome schema stays portable across OpenAI-compatible
providers by using one flat object with ordinary `properties`, `required`, and
a status `enum`. The runtime parser, rather than conditional JSON Schema
combinators, enforces which fields are required or forbidden for each status.
This keeps the contract strict at execution time without relying on every
compatible model to interpret root-level `oneOf` branches consistently.

This outcome protocol is enforced by the child runtime, not only by prompt
wording. When a child tries to finish without a successful outcome report, the
runtime starts up to three bounded repair requests using the transcript that
was already stored. Each request exposes only `report_subagent_outcome` and
uses a reminder to call it, so the model cannot repeat a transfer, write, or
other domain action that already ran. The same instruction remains while the
child writes its concise final answer.
There is no polling or second callback during repair.

If the repair reports `completed`, `incomplete`, or `failed`, that structured value is
authoritative. If the child still omits a valid report, the turn ends after the
bounded repair limit and emits an `incomplete` callback with a fallback summary.
A repair is never retried indefinitely.

## Asynchronous lifecycle

`start_subagent` and `send_subagent_message` return immediately after routing
work. Every `start_subagent` call requires a `continue_after_dispatch` choice:

- Set it to `false` when the parent has no already-planned work outside the
  delegated task that must run immediately. A result with a pending callback
  requests that the current turn end after the complete tool batch succeeds,
  without another provider step or assistant content.
- Set it to `true` only when specific parent work was already planned before
  dispatch, is outside the delegated task, is independent of the callback, and
  must continue immediately.

Every parallel `start_subagent` call in one tool batch must use the same value:
all `false` to wait after the batch, or all `true` to continue independent
parent work. Mixing values is invalid because any `false` call that returns a
pending callback ends the successful batch.

`send_subagent_message` returns control to the parent and applies the passive
post-dispatch policy: continue only qualifying already-planned independent
work; otherwise make no more tool calls or assistant content. The parent must
not narrate waiting, call a response or delivery tool, invent work, retry, redo
delegated work, or poll. Accepted results use
`callback_action: automatic` and `must_wait_for_callback: true`. Duplicate,
already-sent, and callback-pending results use
`callback_action: automatic_existing` because they create no new callback.
The child turn outcome arrives through a separate callback containing:

- parent and child identity;
- `completed`, `incomplete`, or `failed` status;
- structured summary and required next step when available;
- final assistant answer when one exists;
- terminal error when the child failed;
- durable transcript cursor metadata.

Routing results and `active_subagents` expose only identity and lifecycle while
a completion callback is pending. They deliberately omit the child's error,
outcome, summary, and next step until the callback is consumed, so the callback
remains the only authoritative result channel.

Each accepted model-facing start result is deliberately compact:

```json
{
  "accepted": true,
  "callback_action": "automatic",
  "must_wait_for_callback": true,
  "turn_action": "end_turn_wait_for_callback",
  "next_action": "Accepted. The result will arrive automatically later. The current turn will end automatically after this successful tool batch. Do not emit assistant content or make another tool call."
}
```

This example is the `continue_after_dispatch: false` path. With `true`,
`turn_action` is `continue_independent_work` and control returns to the parent
for only the work that justified that choice. Every successful
`start_subagent` call creates a new separately addressed child; it never
selects, reuses, or continues an existing child.

The callback is trusted runtime input, not a human message or a late result on
the completed `start_subagent` call. First try to inject it into a compatible
active parent. Injection waits for a safe provider boundary and never mutates
an in-flight provider request or tool handler. When no compatible run remains,
start a callback continuation:

```go
for callback := range agent.SubscribeSubagentCallbacks(ctx) {
    injected, err := agent.TryInjectSubagentCallback(ctx, callback)
    if err != nil {
        log.Printf("callback injection: %v", err)
        continue
    }
    if injected {
        continue
    }
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

## Creation and follow-up behavior

Every `start_subagent` call creates a new child. The default is sequential:
ordinary lookup or research starts one child, evaluates its callback, and
starts another only when the result is insufficient. Multiple starts in one
provider response are reserved for genuinely independent comparison or
parallel work. To continue an incomplete or failed child, wait until it is idle
and its latest callback has been consumed, then call `send_subagent_message`
with its stable ID. Completed children are never reused. Completed and failed
children are automatically closed when their response scope ends; incomplete
children remain available for follow-up.

## Queued follow-ups

The model-facing `send_subagent_message` starts immediately only when an
incomplete or failed child is idle and its latest callback was consumed. A
running child returns
`callback_pending` without accepting or queuing the message. Direct Go and HTTP
callers may still queue FIFO input behind a running child. An observed completed
child returns `child_completed` without dispatch and cannot be reused. The
parent has no
model-facing list or status tools. The runtime supplies active instance
summaries through `active_subagents`, and the parent receives child answers
only through callbacks. When a callback is pending, the reminder marks
`completion_callback: pending` without copying its outcome payload.

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
input from direct Go and HTTP callers. An idle `incomplete` or `failed` child
accepts a focused follow-up or recovery instruction only after its latest
callback was consumed. A completed child rejects new messages. For the
model-facing tool, a pending callback is an expected controlled result rather
than a failed tool result:

```json
{
  "action": "callback_pending",
  "accepted": false,
  "callback_action": "automatic_existing",
  "must_wait_for_callback": true,
  "instruction": "Not accepted. The pending result will arrive automatically later. Continue only work already planned before dispatch that is outside the delegated task and independent of its callback. If none remains, end the turn immediately without assistant content or another tool call."
}
```

The result creates no new callback and does not authorize a retry. An observed
completed child similarly returns `action: child_completed`,
`callback_action: none`, and `must_wait_for_callback: false`; deliver the
completed result instead of messaging that child again. Tool calls
already in the same parallel batch still all run before AgentRuntime evaluates
the batch. Direct Go and HTTP sends continue returning the callback-pending
lifecycle error, while closed and outcome-less children remain rejected.

Routine lifecycle cleanup happens at the response-scope boundary. One human
user message opens one response scope. Accepted child dispatches keep that
scope open. A callback first tries to join a compatible active parent at its
next provider boundary; otherwise its continuation remains in the same scope.
Inline delivery holds a pending-input barrier until the trusted message is
durably appended. A follow-up adds another pending callback. The final boundary
requires one active turn with no callback or pending input. The initial-action
guard applies only to provider step one of the human root turn; a callback
continuation can deliver a final tool on its first provider round. Earlier
`EndResponseScope` calls are successful non-executing skips and are not retained
for later delivery. If the tool uses `EndTurnOnSuccess`, that skipped call still
ends the current turn while a pending callback keeps the response scope open,
without another provider round. If the scope has no pending callback or other
active turn, the initial premature call continues so ordinary work remains
available. The model-facing contract tells the model not to retry that call;
normal completion repair requests the final delivery. The coordinator still
accepts a quiescent later provider-round delivery for compatibility.

Each trusted callback includes `callback_progress`, an atomic response-scope
snapshot. `pending_callbacks` lists accepted dispatches that are still
outstanding with their subagent ID, definition, display name, dispatch ID, and
child turn ID when available. `received_callbacks` lists callback turns already
delivered with the same child identity and `outcome_status`. The parent uses
this snapshot to avoid duplicate dispatches, continue only independent work
while results remain outstanding, and combine the complete outcome set before
final delivery.

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

Context reminders remain stable, while the explicit trusted callback can be
inserted between provider rounds. That callback is authoritative input; other
durable lifecycle changes still appear through the normal reminder snapshot.

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
