---
title: Skills & Task Agents
sidebar_position: 3
---

# Skills and Task Agents

Skills add reusable instructions. Task agents add isolated child sessions with
their own model, tools, skills, and transcript. Both are project files loaded
by `agentcli.LoadProject`.

## Automatic selection by description

The main model initially sees the available skill and task-agent names plus
their descriptions. Users do not need to mention a capability by name. When a
prompt matches a description, the model can select it automatically:

- a matching skill causes the model to call `load_skill`, then follow the
  loaded instructions in the current turn;
- a matching task agent causes the main model to call `task`, creating an
  isolated task session for that work.

This selection is a model decision, not a deterministic keyword router. Write
descriptions as clear trigger conditions—start with language such as “Use when
...” and state both the work that should match and important cases that should
not. Explicitly naming a skill or task agent also works, but is not required.
Only capabilities allowed for the current agent are available for selection.

## Skills

Create `.agentcli/skill/<name>/SKILL.md`:

```md
---
name: source-review
description: Use when reviewing evidence or separating facts from inference.
---

Check primary sources first. State uncertainty and conflicting evidence.
```

Allow the skill in `MAIN.md` or a task-agent definition:

```yaml
skills:
  - source-review
```

When the prompt matches the skill description, the model uses the framework
`load_skill` tool to load the full body. A skill adds instructions only; it
does not register or enable application tools.

Tools can require a skill to be loaded in the current turn:

```go
RequiredSkills: []string{"source-review"},
```

If it is missing, the handler does not run and the model receives a result
telling it to load the skill and retry. AgentCLI suppresses recent duplicate
bodies and refreshes old instructions according to `WithSkillReloadPolicy`.

## Task-agent definition

Create `.agentcli/agent/<directory>/<name>.md`:

```md
---
name: researcher
description: Use when work requires substantial research or source comparison.
provider: primary
model: your-model
skills:
  - source-review
tools:
  - glob
  - read
---

Investigate the evidence, state uncertainty, and return a concise conclusion.
```

`name`, `description`, `provider`, and `model` are required. The Markdown body
defines the specialist role and quality bar. Framework-owned instructions
handle task schemas, delivery, and safety boundaries. When the current prompt
matches this description and specialist work is useful, the main model can
spawn it without the user writing “use researcher.”

## Run and resume tasks

The main model receives one `task` tool. Child agents never receive it, so
tasks cannot nest.

New task:

```json
{"agent":"researcher","description":"compare options","prompt":"Compare A and B."}
```

Resume the same retained conversation:

```json
{"task_id":"subagent_123","prompt":"Now include option C."}
```

Foreground is the default and returns the result in the current main-agent
turn. Independent task calls in one tool-call batch run concurrently. Use
`background: true` only when later delivery is useful; AgentCLI delivers the
terminal result exactly once without a client-side polling loop.

Task states are `running`, `completed`, `incomplete`, and `error`. An exact
task ID remains resumable after a terminal run. Unknown, closed, or currently
running IDs fail instead of silently creating replacements.

## Structured results

A definition may require a final JSON result:

```yaml
result:
  message_field: message
  metadata:
    requires_requester_reply:
      type: boolean
      required: true
```

The message becomes task output. Validated string/boolean metadata is exposed
to the host through completion events but is not inserted into model context.

## Host controls and retention

Go, Terminal, and nested HTTP routes can inspect task transcripts, follow
runs, send direct input, resolve safety decisions, interrupt work, and close a
task session. These are host controls, not model tools.

Closing a task is explicit and idempotent. It interrupts active work, drops
queued input, rejects future sends, and retains transcript/run history. Normal
completion does not close a task. Use `WithSubagentStorage` when task records
must survive process restarts.
