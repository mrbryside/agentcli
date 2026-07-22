---
title: Subagent views
sidebar_position: 4
---

# Subagent views

The terminal can display a root session and its child sessions without mixing
their transcripts. Each child has its own session, turns, messages, live
stream, tool events, permission requests, and confirmation requests.

## Discover child sessions

Run:

```text
/agents
```

The result has two groups:

- **Available subagents** are definitions the root agent may start.
- **Child sessions** are existing instances, including their random display
  name, definition, status, label, and queued-message count.

The terminal does not create a child merely because `/agents` was called. Ask
the root agent to delegate a suitable task; the root decides whether to call
the subagent tool.

## Open and leave a child

Open a child by ID or display name:

```text
/agent Sol
```

The terminal loads that child's stored messages and attaches to its current
run when it is still streaming. From that point, ordinary prompt input is sent
to the child rather than the root.

Return to the root:

```text
/back
```

`/back` only detaches the child view. It does not cancel the child's turn.
Reopen the child later to restore its transcript and continue its live stream.

## Send a follow-up

While the child view is selected, enter an ordinary prompt:

```text
❯ Compare that with the storage implementation too.
```

If the child is idle, AgentCLI starts a new child turn. If it is running, the
message is queued and processed by that same child instance. This avoids
starting a duplicate child for a conversational follow-up.

Use this command for a lightweight status check:

```text
/agent-status Sol
```

It reports lifecycle state and a compact activity summary. It does not read
the complete child transcript or cause a parent-agent turn.

## Background completion and callbacks

Subagents always run asynchronously relative to the root. When a child turn
ends, AgentCLI sends a `completed`, `incomplete`, or `failed` callback to the
root. The callback contains child identity, structured summary/next-step
information, and the final result or failure information. `idle` only means no
turn is executing; it does not imply that the delegated task is complete.

If the root is already running, the callback is queued. The root may act on a
completed child while other children continue, follow up on an incomplete
child, or wait for more callbacks.
The terminal displays callback notifications in the root view without copying
child output into the selected child view.

## Close a child

Close an instance when its task and follow-ups are finished:

```text
/close Sol
```

An active child must be interrupted before it can be closed. Closing an idle
child preserves its stored transcript but changes its lifecycle state to
closed.

## View isolation

Root and child views share the same renderer features:

- Markdown streaming
- spinner-only loading state
- collapsed provider reasoning and `Ctrl+O`
- multiline input and bracketed paste
- prompt history with Up and Down
- `Esc` interruption

Only the selected view writes visible content. A root or child that continues
in the background retains events for replay when its view is opened again.
