# agentcli

`agentcli` is a Go library for building provider-neutral AI agents with
streaming, tools, safety gates, persistent conversations, task agents, and
multiple client surfaces.

Use the built-in Terminal UI for local agents, or expose the same runtime over
HTTP and SSE. The runtime stays independent from any one model provider.

[Documentation](https://mrbryside.github.io/agentcli/) ·
[Go API](https://pkg.go.dev/github.com/mrbryside/agentcli) ·
[HTTP API](https://mrbryside.github.io/agentcli/api-reference)

## Features

- Event-sourced agent runs with streaming output and resumable sessions.
- Structured tools with schemas, permissions, confirmations, and guardrails.
- Skills and task agents for focused or parallel work.
- Automatic context compaction for long conversations.
- Interactive Terminal UI plus Echo HTTP and reconnectable SSE APIs.
- Runtime logging and optional Langfuse tracing.

## Quick start

You need Go `1.26.3` or newer and an OpenAI-compatible API endpoint.

### 1. Create a project

Run the starter installer:

```sh
curl -fsSL https://raw.githubusercontent.com/mrbryside/agentcli/main/init/install.sh | sh
```

It asks for a new folder and Go module path, then creates a small read-only
agent with `glob` and `read` tools.

### 2. Configure the model

Enter the new project and edit these files:

- `.agentcli/config.yaml` — replace `replace-provider` and `replace-model`.
- `.agentcli/MAIN.md` — use the same provider and model names.

Keep the API key in your environment:

```sh
cd my-agent
export API_KEY='your-api-key'
```

### 3. Run the agent

```sh
go run .
```

You can also run one prompt without opening the interactive input loop:

```sh
go run . "Explain this project"
```

The generated project is intentionally small:

```text
my-agent/
├── main.go
├── tool_glob.go
├── tool_read.go
└── .agentcli/
    ├── config.yaml
    └── MAIN.md
```

See [Getting Started](https://mrbryside.github.io/agentcli/)
for provider configuration and generated-file details.

## Add agentcli to an existing Go app

Install the module:

```sh
go get github.com/mrbryside/agentcli
```

Load a project and start the Terminal UI:

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

return agent.RunTerminal()
```

Register application tools with `agentcli.WithTool(...)`. Project files decide
which registered tools, skills, and task agents each agent may use.

## Project files

| Path | Purpose |
| --- | --- |
| `.agentcli/config.yaml` | Providers, permissions, compaction, logging, and observability. |
| `.agentcli/MAIN.md` | Main-agent model, allowed capabilities, and instructions. |
| `.agentcli/skill/*/SKILL.md` | Optional reusable skill instructions. |
| `.agentcli/agent/*/*.md` | Optional task-agent definitions. |

For the full format, see
[Project configuration](https://mrbryside.github.io/agentcli/getting-started/project-configuration).

## Client surfaces

Run an interactive terminal:

```go
err := agent.RunTerminal()
```

Run the HTTP and SSE server:

```go
err := agent.RunServer(
    agentcli.WithServerAddress("127.0.0.1:8080"),
)
```

Useful Terminal shortcuts:

- `Ctrl+O` expands or collapses model reasoning.
- `Ctrl+L` opens or closes the runtime-log view.
- `Esc` interrupts the active response.
- Press `Ctrl+C` twice to exit.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
```

Repository helpers:

```sh
make terminal    # run the example Terminal app
make docs        # start the documentation site
make docs-build  # regenerate and build documentation
```

More examples and API details are available in the
[published documentation](https://mrbryside.github.io/agentcli/) and the
[`documentation/`](documentation/) source directory.
