---
title: Subagent views
sidebar_position: 4
---

# Subagent views

Terminal child-session views are host controls, not the model-facing protocol.
The main model sees only `task`: foreground output returns in its current turn,
while background and foreground-wait-promoted completion is delivered exactly
once by Agent. Terminal must not poll, inject, or continue a task result.

The terminal can display a main-agent session and its subagent sessions without mixing
their transcripts. Each subagent has its own session, turns, messages, live
stream, tool events, permission requests, and confirmation requests.

## Discover subagent sessions

Run:

```text
/agents
```

The result has two groups:

- **Available subagents** are definitions the main agent may start.
- **Subagent sessions** are existing instances, including their random display
  name, definition, status, label, and queued-message count.

The terminal does not create a subagent merely because `/agents` was called. Ask
the main agent to delegate a suitable task; the main agent decides whether to call
the subagent tool.

## Open and leave a subagent

Open a subagent by ID or display name:

```text
/agent Sol
```

The terminal loads that subagent's stored messages and attaches to its current
run when it is still streaming. From that point, ordinary prompt input is sent
to the subagent rather than the main agent.

Return to the main-agent view:

```text
/back
```

`/back` only detaches the subagent view. It does not cancel the subagent's turn.
Reopen the subagent later to restore its transcript and continue its live stream.

## Send a follow-up

While the subagent view is selected, enter an ordinary prompt:

```text
❯ Compare that with the storage implementation too.
```

If the subagent is idle, AgentCLI starts a new subagent turn. If it is running, the
message is queued and processed by that same subagent instance. This avoids
starting a duplicate subagent for a conversational follow-up.

Use this command for a lightweight status check:

```text
/agent-status Sol
```

It reports lifecycle state and a compact activity summary. It does not read
the complete subagent transcript or cause a main-agent turn.

## Background completion and results

Subagents always run asynchronously relative to the main agent. When a
subagent turn ends, AgentCLI sends a `completed`, `incomplete`, or `failed`
result to the main agent. The result contains subagent identity, structured summary/next-step
information, and the final result or failure information. `idle` only means no
turn is executing; it does not imply that the delegated task is complete.

If the main agent is already running, the result is queued. The main agent may act on a
completed subagent while other subagents continue, follow up on an incomplete
subagent, or wait for more results.
The terminal displays result notifications in the main-agent view without copying
subagent output into the selected subagent view.

The Terminal and its repository playground use the same main-agent `task`
contract; they do not maintain a separate schema. Foreground task output
returns in the current main turn. Background and foreground-wait-promoted work
returns `running`, then Agent performs exactly-once delivery at a safe boundary
or through one continuation turn. Terminal renders that normal main-agent
activity and never polls, injects, or continues task results. Destructive close
remains a Terminal/application command and is not exposed to the model.

## Close a subagent

The runtime automatically closes completed and failed subagents at the end of
their settled response scope. Incomplete subagents remain available for
follow-up. Use `/close` only when the user explicitly wants to stop, discard,
or close a subagent immediately:

```text
/close Sol
```

The command can interrupt a running subagent and drops queued subagent messages.
Closing preserves the stored transcript but changes its lifecycle state to
closed, so the view remains available as read-only history. It also cancels
outstanding result obligations for that subagent so the main agent response scope
can reach a later final completion boundary. The command itself does not start
a provider turn or produce a model response. See
[Subagent lifecycle control](../capabilities/subagent-lifecycle-control.md).

## View isolation

Main-agent and subagent views share the same renderer features:

- Markdown streaming
- spinner-only loading state
- collapsed provider reasoning and `Ctrl+O`
- multiline input and bracketed paste
- prompt history with Up and Down
- `Esc` interruption

Only the selected view writes visible content. A main agent or subagent that
continues in the background retains events for replay when its view is opened
again.
