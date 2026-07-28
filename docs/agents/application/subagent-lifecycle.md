# Subagent lifecycle control

Read this file when changing explicit subagent close, response-scope result
accounting, automatic subagent cleanup, or application lifecycle surfaces.

## Ownership

The main-agent model catalog contains only `start_subagent` and
`send_subagent_message`. Lifecycle summaries are injected through
`active_subagents`; list and status remain application-owned surfaces. Subagents
receive `report_subagent_result`. Destructive close belongs to the host
application and is available through `Agent.CloseSubagent`, Terminal `/close`,
and the HTTP `DELETE` subagent endpoint.

`active_subagents` is lifecycle-only while a result is pending. It emits
`result_delivery=pending` but withholds the structured result payload until
the result has been consumed.

All explicit surfaces converge on `subagentManager.CloseSubagent`. The manager
validates main agent ownership, durably closes storage, removes queued input,
interrupts active work when necessary, closes the subagent runtime, cancels
response-scope result obligations, signals state change, and publishes
`SystemSubagentClosed`. Transcript and retained run history remain readable.

## Result accounting

`ResponseScopeCoordinator.RegisterAssignment` adds one queued assignment and
increments both `pendingResults` and the scope's per-subagent touch count.
`ReserveResultTurn` removes the oldest matching assignment, decrements
`pendingResults`, and transfers responsibility to an active result turn.
`ReserveInlineResult` instead transfers responsibility to `pendingInputs`
on an already-active compatible turn. The runtime commits it only after the
trusted result is durably appended at a provider boundary. Rollback restores
the original assignment if the run closes before acceptance.

Assignment registration retains the subagent definition, display name, assignment ID,
and subagent turn ID when available. Result reservation atomically records the
received subagent turn and result status alongside the remaining pending assignments.
The reservation exposes this snapshot as `result_progress` for the trusted
runtime message. Rollback removes the received entry before restoring the
pending obligation.

`CancelSubagentAssignments` records a terminal cancellation marker for the
session/subagent pair and deletes every queued, unreserved assignment. Each deleted
assignment decrements `pendingResults` and the matching touch count once.
After cancellation:

- `RegisterAssignment` creates no new obligation for that subagent;
- an assignment rollback cannot find and decrement a deleted assignment;
- a rejected result reservation removes its result record but does not
  restore the assignment or increment `pendingResults`;
- an already committed result turn remains active and settles normally.

Cancellation is idempotent. Result tombstones and subagent cancellation markers
outlive response-scope deletion because subagent IDs are stable and late delivery
must not consume work from another scope.

Cancellation changes accounting only. It does not start a provider turn.
`EndResponseScope` handlers still require one active turn, zero pending
results, and zero pending runtime inputs. Provider step one is blocked only
for the initial human main-agent turn; a result continuation may execute there on
its first provider round.

## Automatic cleanup

At the final response-scope boundary, `autoCloseScopeSubagents` closes idle
`completed` and `failed` subagents that are exclusive to that scope.
`incomplete`, running, queued, result-pending, and cross-scope subagents remain
open. Cleanup runs before final handlers and successful closes publish
`SystemSubagentClosed`.

Back to [application/index.md](index.md).
