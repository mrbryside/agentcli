---
title: Events and history
sidebar_position: 2
---

# Events and history

Events describe runtime activity; messages describe conversation history. Keep
the two concepts separate in UI state.

## Live subscription

`Run.Subscribe(ctx)` is live-only. It returns:

- `Cursor`: the highest retained event already committed when the subscription
  was installed.
- `Events`: events committed after that cursor.

It does not replay old events automatically.

## Gap-free reconnection

Subscribe first, backfill through the subscription fence, then consume live
events:

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

Persist `lastCursor` for the exact `(sessionID, turnID)` view. Do not share one
cursor across sessions or child views.

## Important event types

| Event | Meaning |
| --- | --- |
| `run_started` | The turn is active; includes the current permission mode. |
| `provider_event_received` | Content, reasoning, tool fragments, completion, or provider error arrived. |
| `tool_call_requested` | A complete tool call entered execution routing. |
| `tool_result_received` | The correlated result returned to the runtime. |
| `permission_requested` | A custom capability needs a caller decision. |
| `permission_resolved` | A correlated permission decision was accepted. |
| `confirmation_requested` | A tool needs invocation-specific Yes/No confirmation. |
| `confirmation_resolved` | A correlated Yes/No answer was accepted. |
| `permission_mode_changed` | The global policy mode changed during an active run. |
| `agent_interrupted` | Interruption propagated through the turn. |
| `run_completed` | A successful final result is available. |
| `run_failed` | Infrastructure, provider, storage, or loop execution failed. |

Permission and confirmation cancellation/expiry events are also retained.

## Provider events

For `ProviderEventReceived`, `event.ProviderEvent` is a value in the generic
provider domain. Branch on its type:

```go
switch event.ProviderEvent.Type {
case provider.ContentReceived:
    renderText(event.ProviderEvent.Content)
case provider.ReasoningReceived:
    renderReasoning(event.ProviderEvent.Reasoning)
case provider.ToolCallStarted, provider.ToolArgumentsReceived, provider.ToolCallCompleted:
    renderToolFragment(event.ProviderEvent.Tool)
}
```

The outer runtime event is always populated consistently for its type; the HTTP
representation uses optional nested fields because a single stable JSON
envelope represents every event variant.

## UI ownership

Maintain one render buffer and streaming flag per session/turn view. Switching
to a child session should render only that child's retained messages and live
events. A background stream continues through its own subscription even while
another view is visible.
