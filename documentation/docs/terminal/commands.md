---
title: Command reference
sidebar_position: 3
---

# Command reference

Commands begin with `/`. Any other submitted text becomes a user message for
the currently selected main-agent or retained task-session view.

## General commands

| Command | Description |
| --- | --- |
| `/help` | Print the command and keyboard-shortcut summary. |
| `/session` | Show the main-agent session, selected task session when applicable, and selected-view streaming state. |
| `/new` | Generate and switch to a new main-agent session. The Agent remains open. |
| `/clear` | Clear the screen and redraw the banner for the current main-agent session. |
| `/skills` | List skills available for automatic selection. This does not load a skill. |
| `/exit` | Leave the terminal without closing the Agent. |
| `/quit` | Alias for `/exit`. |

`/new` clears queued main-agent prompts but does not delete the old session or its
stored transcript. The old session can still be inspected through the Go or
HTTP APIs.

## Task-session commands

| Command | Description |
| --- | --- |
| `/agents` | List available task-agent definitions and retained task sessions. |
| `/agent REF` | Open a retained task session using its ID or case-insensitive display name. |
| `/agent-status REF` | Show task result and session lifecycle without starting a model turn. |
| `/back` | Return to the main-agent view without interrupting the task session. |
| `/close REF` | Destructively close a task session, interrupting active work if needed, dropping queued input, and releasing outstanding result obligations. |

The command spellings remain backward compatible even though the UI now uses
the task contract's terminology. These are host controls for persisted task
sessions; the main model still sees only the `task` tool. `REF` is one task ID
or one display name. See [Task-session views](./subagent-views.md) for message
routing and background work.

## Permission commands

| Command | Description |
| --- | --- |
| `/mode` | Show the current permission mode. |
| `/mode MODE` | Change the live permission mode. |
| `/permissions` | List unresolved permission requests. |
| `/allow ID` | Allow one pending invocation. |
| `/allow-session ID` | Allow the capability for the current session. |
| `/allow-project ID` | Allow the capability for the current project. |
| `/deny ID` | Deny a pending invocation. |

Supported values for `MODE` are:

```text
default
acceptEdits
criticalOnly
dontAsk
plan
unrestricted
```

When a permission prompt is the single visible approval, numeric shortcuts
answer it:

| Input | Decision |
| --- | --- |
| `1` | Allow once |
| `2` | Allow for this session |
| `3` | Allow for this project |
| `4` | Deny |

Use the explicit ID commands when requests from several main-agent or task
sessions are pending.

## Confirmation commands

| Command | Description |
| --- | --- |
| `/confirmations` | List unresolved Yes/No confirmations. |
| `/confirm ID` | Answer Yes to one confirmation. |
| `/decline ID` | Answer No to one confirmation. |

Typing `y` or `n` answers the visible confirmation. Main-agent/task-session
permissions and confirmations share one global FIFO, so only the oldest request is actionable.
A shortcut for the wrong kind is ignored rather than resolving another queued
request. Confirmations are never bypassed by unrestricted mode.

## Interrupt and exit behavior

`Esc` interrupts only the active response in the selected view. The session
and terminal stay open.

The first `Ctrl+C` displays an exit warning. Press `Ctrl+C` again within two
seconds to exit. This prevents an accidental interrupt key from immediately
closing an interactive session. `/exit` and `/quit` exit immediately.
