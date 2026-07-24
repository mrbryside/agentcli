# agentcli

`agentcli` is a Go package for building AI agent applications without wiring
the runtime, tool workers, conversation storage, permissions, confirmations,
skills, and subagents manually.

Read the [AgentCLI documentation](https://mrbryside.github.io/agentcli/) for
guides, examples, HTTP API details, and the SSE event reference.

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

### Scaffold a terminal agent project

The bootstrap script creates a minimal terminal application plus a `.agentcli`
project with an example skill and researcher subagent. The main agent receives
bounded `glob`, `read`, and double-gated exact-match `edit` tools, plus the
network-free `report_discord` finalizer with a pre-execution prompt tool-call
guard; the researcher stays read-only with `glob` and `read`. `read` returns at
most 2,000
lines and a `next_offset` when more content remains. Their source is generated
separately as `tool_read.go`, `tool_glob.go`, `tool_edit.go`, and
`tool_report_discord.go`. The finalizer is still called once at the end of every
turn, but the agent decides whether a report is useful: omitting
`skipReport` or setting it to `false` records `message`, while
`skipReport: true` returns `skipped` without writing a report entry. A rejected
tool call also leaves the report file unchanged. Reported messages must present
actions, current progress, status, findings, and conclusions directly as the
agent's own work. Useful progress is reported instead of skipped: for example,
`Analyzing main.go to prepare its architecture summary.` The guard rejects
references to delegation, other agents, waiting for them, or promised future
updates and returns feedback with a direct rewrite suggestion. The installer
asks for the project folder name and then the Go module path used in `go.mod`.
It detects the installed Go version for that file, falling back to `1.26.3`
when Go is not installed. Generated projects start in `criticalOnly` permission
mode and read provider credentials only from the process environment. When Go
is available, the installer also runs `go mod tidy` so the project can start
immediately.

```sh
curl -fsSL https://raw.githubusercontent.com/mrbryside/agentcli/main/init/install.sh | sh
```

Then replace the generated provider/model placeholders, set the API key, and
start the app. Go is only needed at this point:

```sh
cd my-agent
export API_KEY='replace-with-a-real-key'
go run .
```

## Project configuration

Create `.agentcli/config.yaml`:

```yaml
permission_mode: default
max_subagents: 4

providers:
  primary:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
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

Applications register raw executable tools with `agentcli.WithTool`. Provide an
explicit `agentcli.ObjectSchema` (or another `agentcli.InputSchema`) and a
`func(context.Context, json.RawMessage) (json.RawMessage, error)` handler;
`agentcli.DecodeArguments` supplies strict object decoding inside the handler.
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

Read the [published documentation](https://mrbryside.github.io/agentcli/) or
browse its [source](documentation/).

```sh
make docs
```

Use `make docs-build` to verify a production documentation build.
