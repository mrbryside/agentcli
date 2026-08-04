---
title: Terminal UI
sidebar_position: 2
---

# Terminal UI

`Agent.RunTerminal` is the reference interactive client. It uses the same
agent, tools, storage, permissions, task sessions, and event stream as the Go
and HTTP surfaces.

```go
project, err := agentcli.LoadProject(".")
if err != nil {
    return err
}

agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithTool(myTool()),
)
if err != nil {
    return err
}
defer agent.Close()

return agent.RunTerminal(
    agentcli.WithTerminalSessionID("manual-test"),
)
```

`RunTerminal` blocks until exit or error, but exiting does not close the
`Agent`.

## Keyboard shortcuts

| Key | Action |
| --- | --- |
| `Enter` | Send the current prompt. |
| `Shift+Enter` | Insert a newline. |
| `Up` / `Down` | Navigate prompt history. |
| `Ctrl+O` | Expand or collapse provider reasoning. |
| `Ctrl+L` | Open or close the runtime-log view. |
| `Esc` | Interrupt the active response without exiting. |
| `Ctrl+C` twice | Exit; the second press must occur within two seconds. |

The editor supports cursor movement, bracketed paste, multiline prompts, and
Markdown streaming without erasing a draft being typed.

Concurrent `task` calls appear as separate progress rows, so each task agent
can finish independently without replacing the status of the others.

## Runtime logs

Enable managed logging when constructing the Agent:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithLogLevel(agentcli.LevelInfo),
)
```

Interactive sessions capture these records instead of printing them into the
conversation. Press `Ctrl+L` or run `/logs` to open a live view of the latest
2,000 records. Press it again to return to the conversation while the run
continues.

The optional `.agentcli/config.yaml` `logging` mapping enables the same managed
logger and is captured by the Terminal as well. `WithLogLevel` applied after
`WithProject` overrides that project setting.

Non-interactive sessions still write managed logs to stderr. A caller-owned
logger passed with `WithLogger` keeps its own output routing.

## Useful commands

| Command | Action |
| --- | --- |
| `/help` | Show commands and shortcuts. |
| `/session` | Show the selected session and streaming state. |
| `/new` | Switch to a new main-agent session. |
| `/clear` | Clear and redraw the current view. |
| `/skills` | List available skills without loading them. |
| `/logs` | Toggle the runtime-log view. |
| `/mode MODE` | Read or change the permission mode. |
| `/permissions` / `/confirmations` | List pending safety decisions. |
| `/agents` | List task-agent definitions and retained task sessions. |
| `/agent REF` | Open a task session by ID or display name. |
| `/agent-status REF` | Show task lifecycle and result. |
| `/back` | Return to the main-agent view. |
| `/close REF` | Close a task session and stop its pending work. |
| `/exit` | Exit immediately. |

Permission prompts accept `1` allow once, `2` allow for the session, `3` allow
for the project, and `4` deny. Confirmation prompts accept `y` or `n`. Use the
explicit `/allow ID`, `/deny ID`, `/confirm ID`, and `/decline ID` forms when
several decisions are pending.

Switching between the main view and a task session changes only what is shown;
it does not interrupt background work. Input submitted while a view is busy is
queued for that same session.

## Playground and one-shot mode

From this repository:

```sh
go run ./playground/terminal
go run ./playground/terminal "Summarize this project"
```

The one-shot form is non-interactive: permissions that would ask are denied
and confirmations are declined. For an embedded one-shot client, combine
`WithNonInteractive(true)` with `WithTerminalInitialPrompt(text)`.

If raw shortcuts do not work, run directly in a TTY. A non-TTY
`WithTerminalInput` uses a line scanner intended for embedding and tests.
