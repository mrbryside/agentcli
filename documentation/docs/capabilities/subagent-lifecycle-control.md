---
title: Subagent lifecycle control
sidebar_position: 3
---

# Subagent lifecycle control

AgentCLI separates model-directed delegation from application-directed
lifecycle control. The model can route work, while the host application owns
inspection and destructive actions.

## Ownership contract

> **v0.1 update:** the main-agent model owns no subagent lifecycle tools. It
> sees only `task`; foreground calls return in place, while background and
> promoted calls are delivered exactly once by `Agent`. The prior model-tool
> table below documents removed names. `/subagents`, Terminal views, and Go
> methods remain host session-management surfaces.

`SystemTaskCompleted` carries task ID, child session/turn, agent, terminal
state, and validated application-only result-contract metadata. The same fact
is `task_completed` on session SSE. Host clients render it but never inject or
continue results themselves.

| Owner | Operations |
| --- | --- |
| Main-agent model | `task` only; new work or same-session idle `task_id` resume |
| Subagent model | Domain tools and one final text response; no task nesting |
| Host application | List/status inspection; explicit close through Go, Terminal, or HTTP; interrupt; history and event presentation |
| Runtime | Result correlation, response-scope accounting, and final-boundary cleanup of eligible completed or failed subagents |

The main-agent model has no destructive subagent-management tool. This keeps a
delegation or result turn from closing work as a side effect of model output.
Bind explicit close to a direct application or user action.

## Explicit close surfaces

All explicit close surfaces converge on `Agent.CloseSubagent` and the same
manager lifecycle:

| Surface | Operation |
| --- | --- |
| Go | `agent.CloseSubagent(ctx, mainAgentSessionID, subagentID)` |
| Terminal | `/close REF` |
| HTTP | `DELETE /v1/sessions/{sessionID}/subagents/{subagentID}` |

The main agent session must own the subagent. A successful close:

1. durably changes the subagent lifecycle to `closed`;
2. removes queued subagent input and rejects future sends;
3. interrupts active subagent work when necessary and closes its runtime;
4. cancels outstanding result obligations for the subagent;
5. publishes `SystemSubagentClosed`.

The transcript and completed run-event history remain available for read-only
views. Close is a lifecycle operation, not data deletion.

## Response-scope accounting

Each accepted subagent assignment increments the originating response scope's
`pendingResults` count and its per-subagent touch count. A result reservation
removes one assignment from the queue, decrements `pendingResults`, and creates
an active result continuation. Under the same coordinator lock, it records
the received subagent identity and result status and snapshots all remaining pending
assignments. That `result_progress` snapshot is injected inside the trusted
result runtime message.

Explicit close makes result cancellation terminal for that subagent:

- every queued, unreserved assignment is deleted;
- each deleted assignment decrements `pendingResults` and the matching
  per-subagent touch count once;
- an assignment registration racing after close creates no obligation;
- if a reserved result fails runtime admission, rollback does not recreate
  the cancelled assignment or increment `pendingResults`;
- ordinary reservation rollback removes the unaccepted received entry and
  restores its original pending assignment;
- a result continuation already admitted by the runtime remains an active
  main agent turn and settles through the normal turn lifecycle.

Repeated close-side cancellation and late assignment rollback are idempotent.
They cannot decrement counters twice or re-open a result barrier.

Cancellation releases response-scope accounting; it does not synthesize a
provider turn or a user-visible response. Final `EndResponseScope` handlers run
when an active main agent turn reaches its completion boundary with
`pendingResults == 0`. An application that closes a subagent while no main agent
turn is active decides separately whether to start another user-visible turn.

## Automatic final-boundary cleanup

When a response scope reaches its final completion boundary, the runtime closes
idle `completed` and `failed` subagents that are exclusive to that scope.
`incomplete`, running, queued, result-pending, and cross-scope subagents remain
open. Automatic cleanup happens before final `EndResponseScope` handlers and
also publishes `SystemSubagentClosed`.

## Events and logs

`Agent.SubscribeSystemEvents(ctx)` emits `SystemSubagentClosed` for successful
explicit and automatic closes. The HTTP session stream retains the equivalent
`subagent_closed` event.

With runtime logging at `debug`, cancelling queued result obligations emits:

```text
msg="response scope result obligations cancelled"
session_id=...
subagent_id=...
cancelled_assignments=...
scope_ids=[...]
```

The record is emitted when at least one queued assignment is cancelled. A close
that only establishes the terminal cancellation state has no queued assignment
count to log.

See [Subagents](./subagents.md) for delegation, result statuses, follow-ups,
and capacity.
