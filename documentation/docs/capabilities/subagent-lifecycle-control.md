---
title: Subagent lifecycle control
sidebar_position: 3
---

# Subagent lifecycle control

AgentCLI separates model-directed delegation from application-directed
lifecycle control. The model can route and inspect work, while the host
application owns destructive actions.

## Ownership contract

| Owner | Operations |
| --- | --- |
| Root model | `start_subagent`, `send_subagent_message`, `list_subagents`, and `subagent_status` |
| Child model | `report_subagent_outcome` |
| Host application | Explicit close through Go, Terminal, or HTTP; interrupt; history and event presentation |
| Runtime | Callback correlation, response-scope accounting, and final-boundary cleanup of eligible completed or failed children |

The root model has no destructive child-management tool. This keeps a
delegation or callback turn from closing work as a side effect of model output.
Bind explicit close to a direct application or user action.

## Explicit close surfaces

All explicit close surfaces converge on `Agent.CloseSubagent` and the same
manager lifecycle:

| Surface | Operation |
| --- | --- |
| Go | `agent.CloseSubagent(ctx, parentSessionID, subagentID)` |
| Terminal | `/close REF` |
| HTTP | `DELETE /v1/sessions/{sessionID}/subagents/{subagentID}` |

The parent session must own the child. A successful close:

1. durably changes the child lifecycle to `closed`;
2. removes queued child input and rejects future sends;
3. interrupts active child work when necessary and closes its runtime;
4. cancels outstanding callback obligations for the child;
5. publishes `SystemSubagentClosed`.

The transcript and completed run-event history remain available for read-only
views. Close is a lifecycle operation, not data deletion.

## Response-scope accounting

Each accepted child dispatch increments the originating response scope's
`pendingCallbacks` count and its per-child touch count. A callback reservation
removes one dispatch from the queue, decrements `pendingCallbacks`, and creates
an active callback continuation.

Explicit close makes callback cancellation terminal for that child:

- every queued, unreserved dispatch is deleted;
- each deleted dispatch decrements `pendingCallbacks` and the matching
  per-child touch count once;
- a dispatch registration racing after close creates no obligation;
- if a reserved callback fails runtime admission, rollback does not recreate
  the cancelled dispatch or increment `pendingCallbacks`;
- a callback continuation already admitted by the runtime remains an active
  parent turn and settles through the normal turn lifecycle.

Repeated close-side cancellation and late dispatch rollback are idempotent.
They cannot decrement counters twice or re-open a callback barrier.

Cancellation releases response-scope accounting; it does not synthesize a
provider turn or a user-visible response. Final `EndResponseScope` handlers run
when an active parent turn reaches its completion boundary with
`pendingCallbacks == 0`. An application that closes a child while no parent
turn is active decides separately whether to start another user-visible turn.

## Automatic final-boundary cleanup

When a response scope reaches its final completion boundary, the runtime closes
idle `completed` and `failed` children that are exclusive to that scope.
`incomplete`, running, queued, callback-pending, and cross-scope children remain
open. Automatic cleanup happens before final `EndResponseScope` handlers and
also publishes `SystemSubagentClosed`.

## Events and logs

`Agent.SubscribeSystemEvents(ctx)` emits `SystemSubagentClosed` for successful
explicit and automatic closes. The HTTP session stream retains the equivalent
`subagent_closed` event.

With runtime logging at `debug`, cancelling queued callback obligations emits:

```text
msg="response scope callback obligations cancelled"
session_id=...
child_id=...
cancelled_dispatches=...
scope_ids=[...]
```

The record is emitted when at least one queued dispatch is cancelled. A close
that only establishes the terminal cancellation state has no queued dispatch
count to log.

See [Subagents](./subagents.md) for delegation, callback outcomes, reuse, and
capacity.
