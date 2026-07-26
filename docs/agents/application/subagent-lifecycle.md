# Subagent lifecycle control

Read this file when changing explicit child close, response-scope callback
accounting, automatic child cleanup, or application lifecycle surfaces.

## Ownership

The root model catalog contains only `start_subagent` and
`send_subagent_message`. Lifecycle summaries are injected through
`active_subagents`; list and status remain application-owned surfaces. Children
receive `report_subagent_outcome`. Destructive close belongs to the host
application and is available through `Agent.CloseSubagent`, Terminal `/close`,
and the HTTP `DELETE` child endpoint.

`active_subagents` is lifecycle-only while a callback is pending. It emits
`completion_callback=pending` but withholds the last-turn outcome payload until
the callback has been consumed.

All explicit surfaces converge on `subagentManager.CloseSubagent`. The manager
validates parent ownership, durably closes storage, removes queued input,
interrupts active work when necessary, closes the child runtime, cancels
response-scope callback obligations, signals state change, and publishes
`SystemSubagentClosed`. Transcript and retained run history remain readable.

## Callback accounting

`ResponseScopeCoordinator.RegisterDispatch` adds one queued dispatch and
increments both `pendingCallbacks` and the scope's per-child touch count.
`ReserveCallbackTurn` removes the oldest matching dispatch, decrements
`pendingCallbacks`, and transfers responsibility to an active callback turn.
`ReserveInlineCallback` instead transfers responsibility to `pendingInputs`
on an already-active compatible turn. The runtime commits it only after the
trusted callback is durably appended at a provider boundary. Rollback restores
the original dispatch if the run closes before acceptance.

`CancelChildDispatches` records a terminal cancellation marker for the
session/child pair and deletes every queued, unreserved dispatch. Each deleted
dispatch decrements `pendingCallbacks` and the matching touch count once.
After cancellation:

- `RegisterDispatch` creates no new obligation for that child;
- a dispatch rollback cannot find and decrement a deleted dispatch;
- a rejected callback reservation removes its callback record but does not
  restore the dispatch or increment `pendingCallbacks`;
- an already committed callback turn remains active and settles normally.

Cancellation is idempotent. Callback tombstones and child cancellation markers
outlive response-scope deletion because child IDs are stable and late delivery
must not consume work from another scope.

Cancellation changes accounting only. It does not start a provider turn.
`EndResponseScope` handlers still require one active turn, zero pending
callbacks, and zero pending runtime inputs. Provider step one is blocked only
for the initial human root turn; a callback continuation may execute there on
its first provider round.

## Automatic cleanup

At the final response-scope boundary, `autoCloseScopeSubagents` closes idle
`completed` and `failed` children that are exclusive to that scope.
`incomplete`, running, queued, callback-pending, and cross-scope children remain
open. Cleanup runs before final handlers and successful closes publish
`SystemSubagentClosed`.

Back to [application/index.md](index.md).
