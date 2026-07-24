---
title: Safety and troubleshooting
sidebar_position: 5
---

# Safety and troubleshooting

The terminal renders permission and confirmation requests produced by custom
tools. It does not add permissions to an unguarded tool automatically; the
tool author must declare the applicable actions and risk.

## Permission modes in the terminal

| Mode | Interactive behavior |
| --- | --- |
| `default` | Ask for guarded invocations. |
| `acceptEdits` | Allow filesystem-write-only tools and ask for other guarded capabilities. |
| `criticalOnly` | Allow low/medium-risk calls and ask for high-risk calls. |
| `dontAsk` | Deny calls that would require a question. |
| `plan` | Deny executable capabilities while planning. |
| `unrestricted` | Allow declared permissions unless an explicit policy rule asks or denies. This is full host access. |

Change the mode with `/mode MODE`. A mode change affects new permission
requests. Existing pending requests remain pending and can be answered by ID.

`unrestricted` is not a sandbox and does not bypass tool confirmations. Read
[Permissions and confirmations](../tools/permissions-and-confirmations.md)
before enabling it.

## Permission or confirmation appears while viewing a child

The request retains the child session, turn, and tool-call correlation. You
may answer it in the selected child view or return to the root and use its ID:

```text
/permissions
/allow perm_...

/confirmations
/confirm confirm_...
```

The Terminal queues root and child permissions and confirmations together and
shows one question at a time. Resolve the visible request to advance the FIFO.
Use ID commands when selecting a known pending request from a listing;
shortcuts apply only to the visible request of the matching kind.

## No visible model output

Run `/session` and check `Streaming`:

- `active` means the selected view still has a live run.
- `idle` means no response is currently streaming in that view.

Use `/back` or `/agent REF` to confirm that the expected view is selected. Root
and child output are intentionally isolated.

If a run failed, the terminal displays the provider or runtime error. The
stored messages and events remain available for inspection.

## Arrow keys print escape text

The interactive editor supports common ANSI, SS3, modified CSI, and Kitty key
encodings. Run the terminal directly in a TTY rather than piping stdin:

```bash
make terminal
```

When `WithTerminalInput` receives a non-TTY reader, AgentCLI uses a line
scanner for deterministic embedding and tests. Raw keyboard shortcuts,
multi-line editing, and terminal cursor control are unavailable in that mode.

## Shift+Enter does not add a line

Modern terminals that support the Kitty keyboard protocol or modified key
reporting send a distinct Shift+Enter sequence. If a terminal maps
Shift+Enter to plain Enter before AgentCLI receives it, configure that terminal
to send `CSI 13;2u` or use bracketed multi-line paste.

## Stop versus exit

- Press `Esc` to interrupt the selected active response and keep working.
- Press `Ctrl+C` once to arm exit and twice within two seconds to quit.
- Run `/exit` or `/quit` to quit immediately.

Leaving the terminal does not close the Agent. Application shutdown should
still call `Agent.Close`.

## One-shot prompts cannot wait for input

`WithTerminalInitialPrompt` does not start the interactive input loop. Build
the Agent with `WithNonInteractive(true)` so permissions that require a user
are denied and confirmations are declined rather than waiting indefinitely:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithNonInteractive(true),
)
if err != nil {
    return err
}

return agent.RunTerminal(
    agentcli.WithTerminalInitialPrompt("Summarize this repository"),
)
```
