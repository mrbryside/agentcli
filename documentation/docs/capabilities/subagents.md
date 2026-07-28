---
title: Subagents
sidebar_position: 2
---

# Subagents

A subagent is a subagent session with its own model, instructions, transcript,
tool/skill allowlists, streaming run, and friendly random display name. The
main agent is the only agent allowed to manage subagents; nesting stops at one
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
trade-offs, and a concise recommendation to the main agent.
```

`name`, `description`, `provider`, and `model` are required. Omit `skills` or
`tools` when none are allowed. Every name and provider is validated during
project loading/agent initialization.

## Main-agent management tools

When definitions exist, the main-agent model receives two fixed framework tools:

| Tool | Use |
| --- | --- |
| `start_subagent` | Create a new subagent and asynchronously route one focused assignment. |
| `send_subagent_message` | Send focused follow-up work to a known subagent. |

Subagents do not receive these management tools. Every subagent instead
receives one framework-owned `report_subagent_result` tool. Before its final
answer, the subagent reports one structured result with a concise summary:

- `completed` means all delegated work is resolved and forbids `next_step`;
- `incomplete` means required work or information remains and requires one
  concrete `next_step`;
- `failed` means a terminal error prevents continuation, requires the actual
  `error`, and forbids `next_step`.

The subagent never invents recovery work merely to populate a result field.
Destructive closure is not model-facing. Applications may close a subagent through
the Go API, Terminal, or HTTP endpoint. See
[Subagent lifecycle control](./subagent-lifecycle-control.md) for the ownership
contract and response-scope accounting.

The model-facing result schema stays portable across OpenAI-compatible
providers by using one flat object with ordinary `properties`, `required`, and
a status `enum`. The runtime parser, rather than conditional JSON Schema
combinators, enforces which fields are required or forbidden for each status.
This keeps the contract strict at execution time without relying on every
compatible model to interpret root-level `oneOf` branches consistently.

This result protocol is enforced by the subagent runtime, not only by prompt
wording. When a subagent tries to finish without a successful result report, the
runtime starts up to three bounded repair requests using the transcript that
was already stored. Each request exposes only `report_subagent_result` and
uses a reminder to call it, so the model cannot repeat a transfer, write, or
other domain action that already ran. The same instruction remains while the
subagent writes its concise final answer.
There is no polling or second result during repair.

If the repair reports `completed`, `incomplete`, or `failed`, that structured value is
authoritative. If the subagent still omits a valid report, the turn ends after the
bounded repair limit and emits an `incomplete` result with a fallback summary.
A repair is never retried indefinitely.

## Asynchronous lifecycle

`start_subagent` and `send_subagent_message` return immediately after routing
work. Every assignment call requires a `continue_main_agent` choice:

- Set it to `false` when the main agent has no already-planned work outside the
  delegated task that must run immediately. A successful tool result returns
  `main_agent_action: stop_and_wait`; the current turn ends after the complete
  tool batch succeeds, without another provider step or assistant content.
- Set it to `true` only when specific main agent work was already planned before
  assignment, is outside the delegated task, is independent of the result, and
  must continue immediately.

Every parallel `start_subagent` call in one tool batch must use the same value:
all `false` to wait after the batch, or all `true` to continue independent
main agent work. Mixing values is invalid because any `false` call that returns a
pending result ends the successful batch.

For `send_subagent_message`, `false` ends an accepted successful tool batch to
wait, while `true` returns control only for qualifying already-planned
independent work. Duplicate, already-sent, and result-pending results end the
successful batch regardless of that choice because they create no new work and
the existing result is already pending. The main agent must not narrate waiting,
call a response or delivery tool, invent work, retry, redo delegated work, or
poll. Accepted calls use `result_delivery: automatic`. Duplicate, already-sent,
and result-pending calls use `result_delivery: existing` because no new work
started and the existing result will arrive automatically.
`recovery_exhausted` uses `result_delivery: none` and
`main_agent_action: report_terminal_failure`, allowing the main agent to report
that the same normalized subagent failure already consumed its single recovery
assignment for this response scope.
The subagent turn result arrives separately and contains:

- main agent and subagent identity;
- `completed`, `incomplete`, or `failed` status;
- structured summary and required next step when available;
- final assistant answer when one exists;
- terminal error when the subagent failed;
- durable transcript cursor metadata.

The main agent receives it as trusted runtime input:

```json
{
  "subagent_id": "subagent_123",
  "display_name": "Vale",
  "definition_name": "researcher",
  "subagent_turn_id": "turn_subagent",
  "status": "completed",
  "summary": "The comparison is complete.",
  "final_answer": "Option A is faster; option B is easier to operate.",
  "result_progress": {
    "pending_count": 0,
    "all_results_delivered": true,
    "pending_results": [],
    "delivered_results": [
      {
        "subagent_id": "subagent_123",
        "definition_name": "researcher",
        "display_name": "Vale",
        "assignment_id": "turn_subagent",
        "subagent_turn_id": "turn_subagent",
        "result_status": "completed"
      }
    ]
  }
}
```

Routing results and `active_subagents` expose only identity and lifecycle while
a result is pending. They deliberately omit the subagent's error, result
status, summary, and next step until the result is consumed, so the result
remains the only authoritative result channel.

Each accepted model-facing start result is deliberately compact:

```json
{
  "subagent_id": "subagent_123",
  "display_name": "Vale",
  "status": "running",
  "action": "created",
  "accepted": true,
  "result_delivery": "automatic",
  "main_agent_action": "stop_and_wait",
  "instruction": "Accepted. The subagent result will arrive automatically. Stop now. The current turn ends automatically while the subagent keeps running. Do not generate assistant content or call another tool."
}
```

This example is the `continue_main_agent: false` path. With `true`,
`main_agent_action` is `continue_independent_work` and control returns to the main agent
for only the work that justified that choice. Every successful
`start_subagent` call creates a new separately addressed subagent; it never
selects, reuses, or continues an existing subagent.

The result is trusted runtime input, not a human message or a late result on
the completed `start_subagent` call. First try to inject it into a compatible
active main agent. Injection waits for a safe provider boundary and never mutates
an in-flight provider request or tool handler. When no compatible run remains,
start a result continuation:

```go
for result := range agent.SubscribeSubagentResults(ctx) {
    injected, err := agent.TryInjectSubagentResult(ctx, result)
    if err != nil {
        log.Printf("result injection: %v", err)
        continue
    }
    if injected {
        continue
    }
    run, events, err := agent.ContinueSubagentResultSubscribed(ctx, result)
    if errors.Is(err, agentcli.ErrTurnInProgress) {
        // Keep the result queued and retry when the main agent session is free.
        continue
    }
    if err != nil {
        log.Printf("result: %v", err)
        continue
    }
    for event := range events.Events {
        render(event)
    }
    _, _ = run.Result()
}
```

Failures also produce results, so a failed subagent cannot leave the main agent
waiting forever without information.

Lifecycle and result status are intentionally separate. `running`, `idle`, and
`closed` describe whether the subagent process can accept work. Result status
describes whether the delegated task is actually resolved:

| Result status | Meaning |
| --- | --- |
| `completed` | The subagent explicitly reported that all required delegated work is resolved. |
| `incomplete` | The turn ended normally, but work, information, or a decision remains. A report still missing after the bounded repair requests defaults here. |
| `failed` | The provider/runtime turn ended with an error. |

The runtime never infers completion merely because the provider stopped
without an error.

## Creation and follow-up behavior

Every `start_subagent` call creates a new subagent. The default is sequential:
ordinary lookup or research starts one subagent, evaluates its result, and
starts another only when the result is insufficient. Multiple starts in one
provider response are reserved for genuinely independent comparison or
parallel work. To continue an incomplete or failed subagent, wait until it is idle
and its latest result has been consumed, then call `send_subagent_message`
with its stable ID. Completed subagents are never reused. Completed and failed
subagents are automatically closed when their response scope ends; incomplete
subagents remain available for follow-up.

## Queued follow-ups

The model-facing `send_subagent_message` starts immediately only when an
incomplete or failed subagent is idle and its latest result was consumed. A
running subagent returns
`result_pending` without accepting or queuing the message. Direct Go and HTTP
callers may still queue FIFO input behind a running subagent. An observed completed
subagent returns `subagent_completed` without assignment and cannot be reused. The
main agent has no
model-facing list or status tools. The runtime supplies active instance
summaries through `active_subagents`, and the main agent receives subagent answers
only through results. When a result is pending, the reminder marks
`result_delivery: pending` without copying its result payload.

The model-facing `send_subagent_message` tool enforces one accepted message per
`(main agent session, main agent turn, subagent)` tuple. Its internal SHA-256 idempotency
key also includes normalized message content:

```text
SHA-256(mainAgentSessionID + mainAgentTurnID + subagentID + normalizedMessage)
```

An exact retry returns `action: duplicate`; a different second message from
the same main agent turn returns `action: already_sent`. Neither invocation starts
a subagent turn or adds mailbox work. This check happens before lifecycle
admission, so even when a fast subagent has already produced a result, a
same-main agent-turn retry remains `duplicate` or `already_sent` instead of becoming
a tool error. A later main agent turn may send again. Direct
application calls to `Agent.SendSubagentMessage` represent explicit UI/user
input and are not restricted by the model-facing main agent-turn guard.

## Results and closing

Results carry only the final assistant answer, not every tool call and
intermediate message. `ListMessages` remains available for a full subagent UI.
`ReadSubagent` is a recovery API that consumes the latest unobserved final
answer; it is not exposed as a model tool.

Sending follows lifecycle admission. A running subagent accepts FIFO mailbox
input from direct Go and HTTP callers. An idle `incomplete` or `failed` subagent
accepts a focused follow-up or recovery instruction only after its latest
result was consumed. A completed subagent rejects new messages. For the
model-facing tool, a pending result is an expected controlled result rather
than a failed tool result:

```json
{
  "action": "result_pending",
  "accepted": false,
  "deduplicated": false,
  "subagent": {
    "id": "subagent_123",
    "display_name": "Vale",
    "definition_name": "researcher",
    "status": "idle",
    "queued_messages": 0
  },
  "result_delivery": "existing",
  "main_agent_action": "stop_and_wait",
  "instruction": "Not accepted. No new work started. Stop now; the existing subagent result will arrive automatically. Do not retry, call another tool, or generate assistant content."
}
```

The call creates no new result and does not authorize a retry. An observed
completed subagent similarly returns `action: subagent_completed`,
`result_delivery: none`, and
`main_agent_action: deliver_completed_result`; deliver the
completed result instead of messaging that subagent again. Tool calls
already in the same parallel batch still all run before AgentRuntime evaluates
the batch. Direct Go and HTTP sends continue returning the result-pending
lifecycle error, while closed subagents without a usable result remain rejected.

Routine lifecycle cleanup happens at the response-scope boundary. One human
user message opens one response scope. Accepted subagent assignments keep that
scope open. A result first tries to join a compatible active main agent at its
next provider boundary; otherwise its continuation remains in the same scope.
Inline delivery holds a pending-input barrier until the trusted message is
durably appended. A follow-up adds another pending result. The final boundary
requires one active turn with no result or pending input. The initial-action
guard applies only to provider step one of the human main-agent turn; a result
continuation can deliver a final tool on its first provider round. Earlier
`EndResponseScope` calls are successful non-executing skips and are not retained
for later delivery. If the tool uses `EndTurnOnSuccess`, that skipped call still
ends the current turn while a pending result keeps the response scope open,
without another provider round. If the scope has no pending result or other
active turn, the initial premature call continues so ordinary work remains
available. The model-facing contract tells the model not to retry that call;
normal completion repair requests the final delivery. The coordinator still
accepts a quiescent later provider-round delivery for compatibility.

Each trusted result includes `result_progress`, an atomic response-scope
snapshot. `pending_results` lists accepted assignments that are still
outstanding with their subagent ID, definition, display name, assignment ID, and
subagent turn ID when available. `delivered_results` lists result turns already
delivered with the same subagent identity, `subagent_turn_id`, and
`result_status`. The main agent uses
this snapshot to avoid duplicate assignments, continue only independent work
while results remain outstanding, and combine the complete result set before
final delivery.

Immediately before final `EndResponseScope` handlers execute, the runtime
reconciles every subagent that accepted work in that scope:

- idle `completed` and `failed` subagents are automatically closed;
- `incomplete` subagents remain open for a later focused follow-up;
- running, queued, result-pending, or otherwise unsettled subagents prevent
  the scope from becoming quiescent;
- a subagent referenced by another live response scope is retained so one scope
  cannot close work owned by another.

Automatic close retains the transcript and run-event history. Successful
closures are grouped into a trusted one-shot context reminder on the next
accepted human user turn. Result turns do not consume this reminder, it is
not persisted as user content, and it tells the model that those subagents can
no longer receive follow-up messages.

Context reminders remain stable, while the explicit trusted result can be
inserted between provider rounds. That result is authoritative input; other
durable lifecycle changes still appear through the normal reminder snapshot.

Application-owned close operations can stop a running subagent or discard an
incomplete subagent. They are available through `Agent.CloseSubagent`, Terminal
`/close`, and the HTTP `DELETE` endpoint, but not through the model tool
catalog. Existing transcript messages and retained run events remain available,
while pending mailbox messages are removed and future sends are rejected.
Closing also makes result cancellation terminal for that subagent: queued
assignments release their response-scope counters, racing registrations are
ignored, and a rejected result reservation cannot restore an obligation.
This releases the result barrier without creating a provider turn or
user-visible response. Bind these destructive surfaces only to explicit
application or user actions. The complete contract is documented in
[Subagent lifecycle control](./subagent-lifecycle-control.md).

## Capacity

Set the project-wide default in `.agentcli/config.yaml`:

```yaml
max_subagents: 4
```

This limits non-closed subagents per main agent session. Omitting the field uses the
default of 4; `0` has the same meaning. `WithMaxSubagents` can override it
programmatically.

```go
agentcli.WithMaxSubagents(4)
```

The bound applies to non-closed subagents per main agent session. Replace the default
relationship storage with `WithSubagentStorage` when subagent metadata must be
durable.
