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

Investigate the assignment and return a self-contained final answer.
```

`name`, `description`, `provider`, and `model` are required. Project loading
validates names, providers, skills, and tool allowlists.

## Main-agent task tool

The main model receives one framework tool, `task`; child agents never receive
it, so task nesting is denied. The removed model-facing names are
`start_subagent`, `send_subagent_message`, `report_subagent_result`, polling,
callbacks, and client-owned result continuation.

For a new task, provide `agent`, `description`, and `prompt`:

```json
{"agent":"researcher","description":"compare options","prompt":"Compare A and B with sources."}
```

For a resume, provide only `task_id` and `prompt`. The ID belongs to its
main-agent session, retains child history, and can resume only while the child
is idle and open. Foreign, unknown, running, and closed IDs fail.

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
child's context, including what it has already gathered and why it asked.

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
messages.

## Capacity

Set the project-wide default in `.agentcli/config.yaml`:

```yaml
max_subagents: 4
```

This limits non-closed child sessions per main-agent session. Omitting the
field, or setting `0`, uses the default of four. `WithMaxSubagents` overrides
it programmatically. Use `WithSubagentStorage` when persisted relationship
metadata must be durable.
