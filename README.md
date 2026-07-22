# agentcli

`agentcli` is a Go package for building AI agent applications without wiring
the runtime, tool workers, conversation storage, permissions, confirmations,
skills, and subagents manually.

It includes two ready-to-use integration surfaces:

- `agent.RunTerminal(...)` for an interactive local playground.
- `agent.RunServer(...)` for an Echo JSON and SSE API.

## Install

```sh
go get github.com/mrbryside/agentcli
```

```go
import "github.com/mrbryside/agentcli"
```

## Project configuration

Create `.agentcli/config.yaml`:

```yaml
permission_mode: default

providers:
  primary:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    request_timeout: 2m
```

Provider names such as `primary` are aliases. The required `type` selects the
adapter; `openai` is currently supported.

Create `.agentcli/MAIN.md`:

```markdown
---
provider: primary
model: gpt-4.1-mini
---

Understand the requested outcome and provide a clear, self-contained result.
```

Omit `tools` or `skills` when none are allowed. Project configuration may also
include `AGENTS.md`, `.agentcli/skill/*/SKILL.md`, and
`.agentcli/agent/*/*.md`.

## Create an Agent

```go
ctx := context.Background()

project, err := agentcli.LoadProject(".")
if err != nil {
    return err
}

agent, err := agentcli.New(ctx, agentcli.WithProject(project))
if err != nil {
    return err
}
defer agent.Close()
```

Applications can add typed executable tools with `agentcli.WithCustomTool`.
Project files only select which registered tools each agent may use.

## Run the terminal playground

```go
err := agent.RunTerminal(
    agentcli.WithTerminalSessionID("manual-check"),
)
```

The included playground registers example `glob`, `read`, and confirmation
tools:

```sh
make terminal
```

## Run the HTTP API

```go
err := agent.RunServer(
    agentcli.WithServerAddress("127.0.0.1:8080"),
)
```

The server exposes JSON commands and reconnectable SSE streams for sessions,
turns, messages, tool activity, permissions, confirmations, and subagents.

## Makefile commands

Run these commands from the repository root:

| Command | Purpose |
| --- | --- |
| `make terminal` | Start the interactive terminal playground. |
| `make docs-install` | Force installation of the Docusaurus dependencies. |
| `make docs` | Install dependencies when needed, then start the Docusaurus development server. |
| `make docs-build` | Install dependencies when needed, regenerate API docs, and build the documentation. |

## Documentation

Detailed guides and API documentation are in [documentation](documentation/).

```sh
make docs
```

Use `make docs-build` to verify a production documentation build.
