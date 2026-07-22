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

When definitions exist, the root model receives five fixed framework tools:

| Tool | Use |
| --- | --- |
| `start_subagent` | Create or reuse a child and asynchronously route work. |
| `send_subagent_message` | Send focused follow-up work to a known child. |
| `close_subagent` | Release an idle child only after its callback has been consumed. It is not cancellation. |
| `list_subagents` | List lightweight child summaries for explicit discovery or selection. |
| `subagent_status` | Read one compact snapshot for an explicit status question; repeated checks in one parent turn return the cached snapshot. |

Child agents do not receive these management tools. Every child instead
receives one framework-owned `report_subagent_outcome` tool. Before its final
answer, the child reports either `completed` or `incomplete` with a concise
summary and, for incomplete work, the required next step.

## Asynchronous lifecycle

`start_subagent` returns immediately after routing work. The parent can perform
other work or end its current answer. The child turn outcome arrives through a
separate callback containing:

- parent and child identity;
- `completed`, `incomplete`, or `failed` status;
- structured summary and required next step when available;
- final assistant answer when one exists;
- terminal error when the child failed;
- durable transcript cursor metadata.

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
| `incomplete` | The turn ended normally, but work, information, or a decision remains. Missing outcome reports default here. |
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
a child turn or adds mailbox work. A later parent turn may send again. Direct
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

Closing is lifecycle cleanup, not cancellation. `CloseSubagent`, the model tool,
and HTTP `DELETE` all reject a `running` child with
`storage.ErrSubagentRunning` / HTTP `409 conflict`. Interrupt the active child
turn first, let its terminal callback transition the child to `idle`, and then
close it. This prevents a premature close from suppressing the callback that
the parent is waiting to consume.

## Capacity

```go
agentcli.WithMaxSubagents(4)
```

The bound applies to non-closed children per parent session. Replace the default
relationship storage with `WithSubagentStorage` when child metadata must be
durable.
