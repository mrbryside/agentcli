---
title: Task-session views
sidebar_position: 4
---

# Task-session views

Terminal task-session views are host controls, not the model-facing protocol.
The main model sees only `task`: foreground output returns in its current turn,
while background and foreground-wait-promoted completion is delivered exactly
once by Agent. Terminal must not poll, inject, or continue a task result.

The terminal can display a main-agent session and its retained task sessions without mixing
their transcripts. Each task agent has its own session, turns, messages, live
stream, tool events, permission requests, and confirmation requests.

The Go host APIs and storage types retain `Subagent` names for compatibility.
The Terminal UI uses task-agent and task-session language so it matches the
model-facing contract without changing those host APIs.

## Discover task sessions

Run:

```text
/agents
```

The result has two groups:

- **Available task agents** are definitions the main agent may start.
- **Retained task sessions** are existing instances, including their random
  display name, definition, lifecycle, latest result, label, and queued-message
  count.

The terminal does not create a task merely because `/agents` was called. Ask
the main agent to delegate a suitable task; the main agent decides whether to call
`task`.

Task-call rendering distinguishes the two model actions:

```text
● task (new · researcher · Compare storage options)
● task (resume · task_...)
```

A successful tool execution then renders the semantic task state—`running`,
`completed`, `incomplete`, or `error`—instead of a generic `done`.

## Open and leave a task session

Open a task session by ID or display name:

```text
/agent Sol
```

The terminal loads that task session's stored messages and attaches to its current
run when it is still streaming. From that point, ordinary prompt input is sent
to that task agent rather than the main agent.

Return to the main-agent view:

```text
/back
```

`/back` only detaches the task-session view. It does not cancel the task turn.
Reopen the session later to restore its transcript and continue its live stream.

## Send a follow-up

While the task-session view is selected, enter an ordinary prompt:

```text
❯ Compare that with the storage implementation too.
```

If the task session is retained and resumable, AgentCLI starts another turn in
that same session. If it is running, the message is queued and processed by the
same task agent. Terminal input never creates a duplicate task session for a
conversational follow-up.

Use this command for a lightweight status check:

```text
/agent-status Sol
```

It reports lifecycle state and the latest task result separately. It does not
read the complete task-agent transcript or cause a main-agent turn.

## Background completion and results

Background task agents run asynchronously relative to the main agent. When a
background turn ends, AgentCLI delivers a `completed`, `incomplete`, or
`error` result to the main agent. The result contains task identity and the
final output or failure information. When a retained task has no active turn,
the UI labels it `resumable`; it never displays the storage layer's blank
status as a separate lifecycle state.

If the main agent is already running, the result is queued. The main agent may act on a
completed task while other tasks continue, follow up on an incomplete task, or
wait for more results.
The terminal displays result notifications in the main-agent view without copying
task-agent output into the selected task-session view.

The Terminal and its repository playground use the same main-agent `task`
contract; they do not maintain a separate schema. Foreground task output
returns in the current main turn. Background and foreground-wait-promoted work
returns `running`, then Agent performs exactly-once delivery at a safe boundary
or through one continuation turn. Terminal renders that normal main-agent
activity and never polls, injects, or continues task results. Destructive close
remains a Terminal/application command and is not exposed to the model.

## Close a task session

Completed and errored task sessions remain retained and resumable after the end of
their settled response scope. Incomplete sessions remain available for
follow-up. Use `/close` only when the user explicitly wants to stop, discard,
or close a task session immediately:

```text
/close Sol
```

The command can interrupt a running task agent and drops queued messages.
Closing preserves the stored transcript but changes its lifecycle state to
closed, so the view remains available as read-only history. It also cancels
outstanding result obligations for that task session so the main agent response scope
can reach a later final completion boundary. The command itself does not start
a provider turn or produce a model response. See
[Subagent lifecycle control](../capabilities/subagent-lifecycle-control.md).

## View isolation

Main-agent and task-session views share the same renderer features:

- Markdown streaming
- spinner-only loading state
- collapsed provider reasoning and `Ctrl+O`
- multiline input and bracketed paste
- prompt history with Up and Down
- `Esc` interruption

Only the selected view writes visible content. A main agent or task agent that
continues in the background retains events for replay when its view is opened
again. `Ctrl+O` uses the same replay path, including runtime alerts and the
currently actionable permission or confirmation.
