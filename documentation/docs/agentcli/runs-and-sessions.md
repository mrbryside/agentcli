---
title: Runs, Sessions & Events
sidebar_position: 1
---

# Runs, Sessions, and Events

- A **session ID** identifies a long-lived conversation transcript.
- A **turn ID** identifies one agent-loop execution in that session.
- A **call ID** identifies one tool invocation in a turn.

Different sessions may run concurrently. Only one direct Go turn may own a
session at a time; another returns `agentcli.ErrTurnInProgress`. The HTTP
server adds a bounded per-session FIFO above this strict runtime behavior.

## Start and stream a turn

```go
run, subscription, err := agent.SendMessage(
    ctx,
    "customer-42",
    "Summarize my previous request.",
)
if err != nil {
    return err
}

for event := range subscription.Events {
    if event.Type == agentcli.ProviderEventReceived &&
        event.ProviderEvent.Type == agentcli.ContentReceived {
        fmt.Print(event.ProviderEvent.Content)
    }
}

result, err := run.Result()
```

`SendMessage` creates IDs and subscribes before `RunStarted`, so a new client
does not miss the beginning of the turn. Use `StartSubscribed` when the caller
needs to supply a full `agentruntime.Request` or its own turn ID.

## Events and transcript

Events describe execution; stored messages describe conversation history.
Keep them as separate UI state.

Common events are:

| Event | Meaning |
| --- | --- |
| `run_started` | The turn was admitted. |
| `provider_event_received` | Content, reasoning, tool fragments, or provider completion arrived. |
| `tool_call_requested` / `tool_result_received` | A validated tool call entered and left execution. |
| `permission_*` / `confirmation_*` | A safety decision changed state. |
| `compaction_*` | Transcript compaction started, completed, or failed. |
| `run_completed` / `run_failed` / `agent_interrupted` | The turn reached a terminal state. |

Provider tool fragments are display data only. Execute work only after the
runtime emits `tool_call_requested` for the complete validated call.

Retrieve the provider-neutral transcript separately:

```go
messages, err := agent.ListMessages(ctx, "customer-42")
```

It stores user, assistant, tool-call, tool-result, trusted runtime, and opaque
compaction-checkpoint messages. Provider SDK types are never persisted.

## Reconnect without gaps

`Run.Subscribe(ctx)` is live-only and returns a cursor fence. Subscribe first,
backfill through that fence, then consume live events:

```go
subscription := run.Subscribe(ctx)

backfill, err := run.EventsBetween(lastCursor, subscription.Cursor)
if err != nil {
    return err
}
for _, event := range backfill {
    render(event)
    lastCursor = event.Cursor()
}

for event := range subscription.Events {
    render(event)
    lastCursor = event.Cursor()
}
```

Store a cursor per `(sessionID, turnID)` view. Do not reuse it across main and
task sessions.

## Status and interruption

`run.Status()` is a snapshot: `active`, `waiting_for_permission`,
`waiting_for_confirmation`, or `done`. Final events and `run.Result()`
distinguish completion, failure, and interruption.

```go
err := run.Interrupt(ctx, "cancelled from UI")
```

Interruption cancels the provider stream, pending safety gates, and active tool
handlers for that turn. Tool handlers should honor `ctx.Done()`.

## Context compaction

Automatic compaction keeps long sessions within the model context window
without deleting their stored transcript. Enable a separate summarizer:

```yaml title=".agentcli/config.yaml"
compaction:
  auto: true
  provider: primary
  model: your-model
```

When a request no longer fits, AgentCLI summarizes an older prefix into an
append-only checkpoint and sends the main model that summary plus recent
verbatim messages. Original messages remain available for auditing and
recovery. Omit the mapping to disable new compactions; an existing checkpoint
is still projected when its session resumes.

The selected model needs context-window and output-token metadata. Exact model
overrides belong under `providers.<name>.models`; otherwise AgentCLI attempts
provider/model metadata discovery and then uses deterministic defaults.

Compaction emits `compaction_started`, `compaction_completed`, or
`compaction_failed`. Checkpoints are internal memory records and should not be
rendered as assistant output.
