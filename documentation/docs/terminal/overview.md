---
title: Terminal overview
sidebar_position: 1
---

# Terminal overview

`Agent.RunTerminal` opens AgentCLI's reference interactive client over an
existing `Agent`. It is useful for testing an agent configuration before
building a web, desktop, or mobile interface.

The terminal is not a separate runtime. It uses the same model, tools,
permission policy, storage, skills, subagents, sessions, turns, and events as
the Go and HTTP APIs.

## Start an interactive terminal

```go
package main

import (
    "context"
    "log"

    "github.com/mrbryside/agentcli"
)

func main() {
    ctx := context.Background()
    project, err := agentcli.LoadProject(".")
    if err != nil {
        log.Fatal(err)
    }

    agent, err := agentcli.New(ctx,
        agentcli.WithProject(project),
        agentcli.WithTool(myTool()),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer agent.Close()

    if err := agent.RunTerminal(
        agentcli.WithTerminalSessionID("manual-test"),
    ); err != nil {
        log.Fatal(err)
    }
}
```

`RunTerminal` blocks until the user exits, input reaches EOF, the Agent is
closed, or an error occurs. Exiting the terminal does not close the Agent.
The caller may inspect messages, start another turn, or run the HTTP server
afterward.

## Run the repository playground

The repository includes a runnable terminal with example `glob`, `read`, and
confirmation tools:

```bash
make terminal
```

This is equivalent to:

```bash
go run ./playground/terminal
```

To run one prompt without opening the interactive editor:

```bash
go run ./playground/terminal "Summarize this project"
```

The playground constructs the Agent with `WithNonInteractive(true)` for this
one-shot form. A permission that would require user input is denied, and a
confirmation is declined instead of waiting forever.

## Terminal options

| Option | Behavior |
| --- | --- |
| `WithTerminalSessionID(id)` | Use a stable root session ID so the transcript can be inspected or continued later. |
| `WithTerminalInput(reader)` | Replace stdin for embedding or scripted tests. |
| `WithTerminalOutput(writer)` | Replace stdout for embedding or output assertions. |
| `WithTerminalInitialPrompt(text)` | Run one prompt and return without starting the interactive editor. |

If no session ID is supplied, AgentCLI generates one. `/new` generates a new
root session while the terminal remains open.

## What to read next

- [Input, streaming, and keyboard shortcuts](./input-and-streaming.md)
- [Complete command reference](./commands.md)
- [Work with subagent views](./subagent-views.md)
- [Safety and troubleshooting](./safety-and-troubleshooting.md)
- [Run the complete playground example](../examples/terminal-playground.md)
