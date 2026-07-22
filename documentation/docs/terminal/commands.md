---
title: Command reference
sidebar_position: 3
---

# Command reference

Commands begin with `/`. Any other submitted text becomes a user message for
the currently selected root or subagent view.

## General commands

| Command | Description |
| --- | --- |
| `/help` | Print the command and keyboard-shortcut summary. |
| `/session` | Show the root session, selected child when applicable, and selected-view streaming state. |
| `/new` | Generate and switch to a new root session. The Agent remains open. |
| `/clear` | Clear the screen and redraw the banner for the current root session. |
| `/skills` | List skills available for automatic selection. This does not load a skill. |
| `/exit` | Leave the terminal without closing the Agent. |
| `/quit` | Alias for `/exit`. |

`/new` clears queued root prompts but does not delete the old session or its
stored transcript. The old session can still be inspected through the Go or
HTTP APIs.

## Subagent commands

| Command | Description |
| --- | --- |
| `/agents` | List available subagent definitions and child sessions. |
| `/agent REF` | Open a child using its ID or case-insensitive display name. |
| `/agent-status REF` | Show lifecycle status and activity summary without starting a model turn. |
| `/back` | Return to the root view without interrupting the child. |
| `/close REF` | Close a completed or failed child after its latest callback has been consumed. It never interrupts work. |

`REF` is one child ID or one display name. See
[Subagent views](./subagent-views.md) for message routing and background work.

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

When a permission prompt is visible, the numeric shortcuts answer the oldest
pending request:

| Input | Decision |
| --- | --- |
| `1` | Allow once |
| `2` | Allow for this session |
| `3` | Allow for this project |
| `4` | Deny |

Use the explicit ID commands when several root or child requests are pending.

## Confirmation commands

| Command | Description |
| --- | --- |
| `/confirmations` | List unresolved Yes/No confirmations. |
| `/confirm ID` | Answer Yes to one confirmation. |
| `/decline ID` | Answer No to one confirmation. |

Typing `y` or `n` answers the oldest pending confirmation. Confirmations are
separate from permissions and are never bypassed by unrestricted mode.

## Interrupt and exit behavior

`Esc` interrupts only the active response in the selected view. The session
and terminal stay open.

The first `Ctrl+C` displays an exit warning. Press `Ctrl+C` again within two
seconds to exit. This prevents an accidental interrupt key from immediately
closing an interactive session. `/exit` and `/quit` exit immediately.
