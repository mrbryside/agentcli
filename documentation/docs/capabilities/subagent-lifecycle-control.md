---
title: Subagent lifecycle control
sidebar_position: 3
---

# Subagent lifecycle control

AgentCLI separates model-directed delegation from application-directed
lifecycle control. The model can run or resume tasks, while the host
application owns inspection and explicit destructive close.

## Retained task contract

Task result state (`running`, `completed`, `incomplete`, or `error`) describes
one call. It does not decide whether the underlying child conversation can be
continued.

The persisted lifecycle has only two named states:

- `running` while a child turn is active;
- `closed` after an explicit host close.

When no turn is active, `status` is omitted from HTTP JSON and the record is
retained/resumable. AgentCLI unloads the live child runtime after a terminal
turn while keeping the task ID, transcript, and last result. There is no
task-count quota.

Resume with the exact task ID. Unknown, closed, and currently running IDs
return `task_not_found`, `task_closed`, and `task_running` respectively; none
creates a replacement task.

## Active background context

At each provider boundary, AgentCLI builds fresh
`<active_background_tasks>` context. It includes only asynchronous tasks whose
result is still pending and exposes `task_id`, `agent`, and `state`
(`running` or `result_pending`). It omits foreground work and finished
resumable records. Models should not poll or duplicate listed tasks, but may
continue independent work.

The reminder is transient runtime state, not compaction history. If a task
finishes while the compaction model is generating a checkpoint, its trusted
result waits in the runtime-input queue. The compacted provider round may keep
the snapshot taken just before compaction, but it cannot complete over that
queued input. AgentCLI then appends `<task_result>` durably, starts the next
provider round, and rebuilds the reminder without the finished task. This
preserves one result across the compaction boundary without teaching the
summarizer live task state.

## Explicit close surfaces

All close surfaces converge on `Agent.CloseSubagent`:

| Surface | Operation |
| --- | --- |
| Go | `agent.CloseSubagent(ctx, mainAgentSessionID, subagentID)` |
| Terminal | `/close REF` |
| HTTP | `DELETE /v1/sessions/{sessionID}/subagents/{subagentID}` |

The main agent session must own the task. The first successful close:

1. stores a durable `closed` tombstone;
2. removes queued input and rejects future sends;
3. interrupts active work when necessary and unloads its runtime;
4. cancels outstanding result obligations;
5. publishes `SystemSubagentClosed`.

Close is idempotent. Repeating it returns the already-closed record without a
second event. Transcript and completed run-event history remain readable.

Normal completion, failure, step-limit finalization, response-scope completion,
and process shutdown retain the task; they do not close it. Graceful shutdown
turns an active run into a retained failed result and clears its process-local
result-delivery identity, avoiding stale `running` or `result_pending` state
after restart. An embedding application may add a later retention job, but
AgentCLI currently performs no automatic task cleanup.

## Response-scope accounting

Each accepted background assignment creates one pending result obligation.
Delivery reserves and then commits that obligation only after the trusted
result is admitted at a provider boundary. Rollback restores an unaccepted
assignment.

Explicit close cancels queued, unreserved obligations exactly once. This
releases the result barrier but does not synthesize a provider turn or
user-visible response. A result turn already admitted by the runtime finishes
normally.

The trusted `<task_result>` contains only `task_id`, `agent`, `state`,
`output`, optional `error_code`, and `error`. Result-contract metadata and
response-scope counters remain application-only.

## Events and logs

`Agent.SubscribeSystemEvents(ctx)` emits `SystemSubagentClosed` only for the
first explicit close. The HTTP session stream retains the equivalent
`subagent_closed` event.

See [Subagents](./subagents.md) for task execution, results, and follow-ups.
