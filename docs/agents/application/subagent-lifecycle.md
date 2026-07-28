# Subagent lifecycle control

The model-facing protocol is `task`: foreground calls wait for the current
result, while background or promoted calls return `running` and Agent delivers
the terminal result exactly once. A task record and transcript stay resumable
by exact same-session `task_id` after every completed, incomplete, or failed
run.

Read this file when changing task retention, background-task reminders,
explicit close, response-scope result accounting, or application lifecycle
surfaces.

## Runtime and retained state

The persisted lifecycle has only two named states:

- `running` means one child turn is active;
- `closed` is an explicit host-owned tombstone.

An empty status means the record is retained and resumable but no live child
turn is running. After a terminal run, Agent unloads the live child runtime and
keeps the task record, transcript, last result, and task ID. Resuming by exact
task ID recreates the runtime from retained state. There is no task-count
quota.

Supplying a task ID never creates a replacement. An unknown ID returns
`error_code=task_not_found`; a closed ID returns
`error_code=task_closed`; an already-running ID returns
`error_code=task_running`.

`<active_background_tasks>` is fresh provider-boundary context containing only
asynchronous work whose result is still pending. It identifies each task by
`task_id`, `agent`, and `state` (`running` or `result_pending`). Foreground
tasks and finished resumable records are omitted. The reminder tells the model
not to poll or duplicate those tasks while allowing independent work to
continue.

Compaction does not summarize this reminder into durable history. The
compacted main-provider request keeps the reminder snapshot resolved for that
boundary. If a task finishes while its summary is being generated, the trusted
task result remains queued outside the compactor. The current provider round
may still see the earlier `running` snapshot, but it cannot finish over the
queued result: completion appends `<task_result>` durably and starts another
provider round. That next round rebuilds the reminder, omits the finished task,
and includes the exact task result once.

## Ownership and explicit close

The main-agent model receives only `task`; list, status, interrupt, and close
remain host application APIs. Destructive close is available through
`Agent.CloseSubagent`, Terminal `/close`, and the HTTP `DELETE` subagent
endpoint.

All close surfaces converge on `subagentManager.CloseSubagent`. The manager
validates ownership, closes durable storage, removes queued input, interrupts
active work when needed, unloads the runtime, cancels outstanding
response-scope result obligations, and publishes `SystemSubagentClosed`.
Transcript and retained run history remain readable. Repeated close is
idempotent and returns the same closed record without publishing another close
event.

Normal completion, failure, step-limit finalization, response-scope completion,
and process shutdown do not close a task. Graceful shutdown converts an active
run into a retained failed result and clears its process-local delivery
identity, so restart cannot leave a permanently `running` or
`result_pending` task. Retention cleanup may be added by an embedding
application later; it is not part of the runtime lifecycle.

## Result accounting

`ResponseScopeCoordinator.RegisterAssignment` adds one queued assignment and
increments both `pendingResults` and the scope's per-subagent touch count.
`ReserveResultTurn` removes the oldest matching assignment, decrements
`pendingResults`, and transfers responsibility to an active result turn.
`ReserveInlineResult` instead transfers responsibility to `pendingInputs` on
an already-active compatible turn. The runtime commits it only after the
trusted result is durably appended at a provider boundary. Rollback restores
the original assignment if the run closes before acceptance.

The trusted `<task_result>` message contains only `task_id`, `agent`, `state`,
`output`, optional `error_code`, and `error`. Result-contract metadata and
response-scope progress remain application/runtime-only.

`CancelSubagentAssignments` records a terminal cancellation marker for the
session/subagent pair and deletes every queued, unreserved assignment. Each
deleted assignment decrements `pendingResults` and the matching touch count
once. Cancellation is idempotent and changes accounting only; it does not
start a provider turn.

`EndResponseScope` handlers still require one active turn, zero pending
results, and zero pending runtime inputs. A result continuation may execute a
required final handler on its first provider round.

Back to [application/index.md](index.md).
