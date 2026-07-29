---
title: Subagents
sidebar_position: 2
---

# Subagents

A subagent is a persisted child session with its own model, instructions,
transcript, tool/skill allowlists, streaming run, and display name. The
session and host-management surfaces retain the word **subagent**. The model
execution protocol is named **task**.

## Definition

Create `.agentcli/agent/<directory>/<name>.md`:

```markdown
---
name: researcher
description: Use for substantial research requiring project inspection or comparison.
provider: openai
model: gpt-4.1-mini
skills:
  - source-review
tools:
  - glob
  - read
---

Investigate the relevant evidence, distinguish findings from inference, and
make the practical trade-offs clear.
```

`name`, `description`, `provider`, and `model` are required. Project loading
validates names, providers, skills, and tool allowlists.

The Markdown body is the agent's domain role, not a transport contract. Put
specialized methods, evidence standards, and output quality criteria there.
The framework separately supplies tool boundaries, no-nesting rules, safe
missing-input behavior, and the final delivery contract. If `result` is
configured, the framework also supplies its exact JSON response shape.

## Main-agent task tool

The main model receives one framework tool, `task`; child agents never receive
it, so task nesting is denied. The removed model-facing names are
`start_subagent`, `send_subagent_message`, `report_subagent_result`, polling,
callbacks, and client-owned result continuation.

Application prompts should stay domain-focused. They can name a configured
agent, require independent or dependent work, or ask to continue the same
agent conversation without repeating the fields and lifecycle below. The
framework system prompt translates that language into the task protocol.

| Instruction owner | What belongs there |
| --- | --- |
| `MAIN.md` and skills | Product/domain policy: when a named specialist is required, which work is independent or sequential, constraints, and the desired outcome. |
| Main-agent framework prompt | `task` selection, new-versus-resume fields, foreground/background behavior, parallel batching, saved-ID continuation, and result interpretation. |
| Concrete `task.prompt` | The current assignment plus all context the child needs for that turn. |
| Subagent definition body | The specialist role, methods, evidence bar, and domain-specific quality criteria. |
| Subagent framework prompt | Capability boundaries, secret safety, no task nesting, one final response, missing-input behavior, and optional result-contract formatting. |

Do not copy tool schemas, `task_id` rules, polling rules, or generic
final-answer boilerplate into application-owned prompts. Keeping these
concerns separate lets framework contract changes apply to existing projects
without requiring prompt rewrites.

For a new task, provide `agent`, `description`, and `prompt`:

```json
{"agent":"researcher","description":"compare options","prompt":"Compare A and B with sources."}
```

For a resume, provide `task_id` and `prompt`. The ID belongs to its main-agent
session, retains child history, and can resume after a completed, incomplete,
or failed run. Presence of `task_id` authoritatively selects resume mode; if a
model repeats create-only `agent` or `description` fields, the runtime ignores
them and continues the identified task. A supplied ID is exact: the runtime
never creates a replacement when it is unknown or closed. Such calls return
`error_code: "task_not_found"` or `error_code: "task_closed"`; a task already
running returns `error_code: "task_running"`.

Foreground is the default. The tool waits for the child final response and
returns:

```json
{"task_id":"subagent_123","agent":"researcher","state":"completed","output":"…","error":""}
```

States are `running`, `completed`, `incomplete`, and `error`. Place independent
task calls in the same tool-call message (one batch) to run them concurrently.
For example, two independent readers require two `task` calls in that one
batch. Use `background:true` only when later delivery is appropriate; do not
poll or simulate waiting.

`WithTaskForegroundWait(d)` can promote unfinished foreground work after a
positive duration. Zero or omission waits without a framework timeout; a
negative duration is invalid. Background and promoted work return `running`.

## Completion and result contracts

The Agent owns exactly-once background/promotion completion. It injects one
trusted `<task_result>` at a safe main-provider boundary or starts one
continuation turn. Terminal, HTTP, and application integrations render normal
main-agent activity; they never operate an injection, polling, or continuation
pump.

At a child provider-step limit, ordinary tools are removed and the child is
asked for one text-only final answer. The runtime returns that partial result
as `incomplete`; there is no report tool or repair loop.

A definition may declare a `result` contract:

```yaml
result:
  message_field: message
  metadata:
    requires_requester_reply:
      type: boolean
      required: true
```

The child emits one matching final JSON object. The message becomes task output.
Validated boolean/string metadata is application-only: it is published in
`SystemTaskCompleted` and HTTP `task_completed`, but never included in provider
context or `<task_result>`. An invalid contract result is one task `error` and
publishes no metadata.

## Requesting essential input

A child may finish with `state: "completed"` and return a question because it
needs essential input before it can safely act. Treat that output as the exact
question to ask the user. Do not guess the missing input and do not perform the
action first.

When the user answers, resume the existing work with exactly the saved task ID
and the user's answer:

```json
{"task_id":"subagent_123","prompt":"The user chose the weekly schedule."}
```

Do not create a new task for this continuation. The existing task retains the
child's context, including what it has already gathered and why it asked. If a
resume call must be corrected, keep the same `task_id`; removing it changes the
call into new-task creation.

## Host session controls

Host code—not the model—may inspect persisted child sessions through Go,
Terminal, or nested `/subagents` routes. Those surfaces support definitions,
list/read views, transcript and run streaming, direct child input,
interruptions, safety decisions, and explicit close. `ListMessages` is the
full transcript API; `ReadSubagent` is an observation/recovery cursor, not a
model-result feed.

Child permission and confirmation requests bubble to the owning main-agent
session. Resolve them using the ownership-scoped Go or HTTP methods. A direct
host message may queue behind a running child according to lifecycle rules;
these host actions are separate from task execution.

Explicit `CloseSubagent`, Terminal `/close`, and the nested HTTP `DELETE`
endpoint preserve transcript/run history while interrupting active work,
dropping queued input, and rejecting future input. A successful close emits
`SystemSubagentClosed`. Terminal task completion separately emits
`SystemTaskCompleted`; both are live system facts rather than transcript
messages. Close is idempotent: closing an already-closed task returns its
existing tombstone without emitting a duplicate close event.

## Retained task sessions

Task sessions have no count quota. After a turn finishes, AgentCLI unloads its
live runtime while retaining the task record and transcript. Any retained task
can be resumed later with its exact `task_id`. Use `WithSubagentStorage` when
those relationships must survive process restarts.

The framework injects `<active_background_tasks>` only while asynchronous work
is still running or its result is waiting for delivery. Finished resumable
tasks and foreground work are omitted. The reminder is rebuilt at each
provider boundary, so stale tasks disappear without a turn-level snapshot.
