---
title: Try the terminal playground
sidebar_position: 3
---

# Try the terminal playground

Every `agentcli.Agent` includes the reference terminal client. Use it to test
project instructions, model behavior, custom tools, permissions,
confirmations, skills, and child views before connecting another UI. The
terminal is a playground over the same Agent; it is not a separate runtime or
the framework UI contract.

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithCustomTool("lookup", "Look up a topic.", lookup),
)
if err != nil {
    return err
}
defer agent.Close()

if err := agent.RunTerminal(
    agentcli.WithTerminalSessionID("manual-check"),
); err != nil {
    return err
}

// /exit ends only the terminal. The Agent remains usable here.
messages, err := agent.ListMessages(ctx, "manual-check")
```

`RunTerminal` blocks until `/exit`, a confirmed Ctrl+C exit, Agent shutdown,
input EOF, or an error. It does not close the Agent. A stable session ID makes
the playground transcript available to later Go API calls; omit the option to
generate a new session ID automatically.

## Terminal options

| Option | Purpose |
| --- | --- |
| `WithTerminalSessionID` | Select a known session for later inspection or continuation. |
| `WithTerminalInput` | Replace stdin, commonly with a scripted reader in a test. |
| `WithTerminalOutput` | Replace stdout, commonly with a buffer in a test. |
| `WithTerminalInitialPrompt` | Run one prompt and return without opening the interactive loop. |

For a one-shot initial prompt that cannot ask the user for tool decisions,
construct the Agent with `WithNonInteractive(true)` as well.

## Repository playground

The executable in `playground/terminal` demonstrates the same API. Its files
contain the caller-owned `glob`, `read`, and `confirm_demo` tools used for
manual testing, while `main.go` only loads the project, registers those tools,
and calls `agent.RunTerminal`.

```bash
go run ./playground/terminal
```

Run a single prompt:

```bash
go run ./playground/terminal "Explain this repository"
```

## Useful commands

| Command | What it tests |
| --- | --- |
| `/new` | Start a fresh root session. |
| `/session` | Show current root/child identity and streaming state. |
| `/skills` | Inspect available skill discovery metadata. |
| `/agents` | List subagent definitions and child instances. |
| `/agent REF` | Open a child by ID or friendly display name. |
| `/agent-status REF` | Read child lifecycle state without a model turn. |
| `/back` | Return to the root view. |
| `/close REF` | Close a child and interrupt active child work. |
| `/permissions` | List unresolved permission requests. |
| `/confirmations` | List unresolved confirmations. |
| `/mode MODE` | Exercise a permission mode. |
| `/clear` | Redraw the active example view. |
| `/exit` | Stop the example. |

Answer the oldest permission with `1` through `4`, or use `/allow ID`,
`/allow-session ID`, `/allow-project ID`, or `/deny ID`.

Answer the oldest confirmation with `y` or `n`, or use `/confirm ID` and
`/decline ID`.

Press `Esc` to interrupt an active root or subagent response while leaving the
playground open. Ctrl+C is reserved for exiting: the first press shows a warning
and the second press within two seconds exits immediately. `/exit` also returns
control to the Go caller without closing the Agent.

The reference terminal keeps assistant Markdown, loading status, and editable
input as independent live state. Every provider content event is appended to
the Markdown source and the current document is rendered again above the input
row. Loading indicators use their own row, so `Thinking` never becomes part of
the `❯` prompt and text being typed remains intact.

For the reusable application design behind root and child screens, see
[Child views](../agentcli/child-views.md). For an HTTP client implementation,
see [Build an application with the API](./api-client-integration.md).
