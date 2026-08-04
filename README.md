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

The bootstrap script creates a minimal read-only terminal application. It
registers only bounded `glob` and `read` tools, and the generated main agent
allowlists those same tools with the complete prompt `You are chatbot.` The
starter has no generated guardrails, edit/report tools, skills, task agents,
logging settings, or observability settings. It asks for the project folder and
Go module path, detects the installed Go version, reads provider credentials
only from the process environment, and runs `go mod tidy` when Go is available.

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

# Optional structured runtime, tool, scope, and repair lifecycle logs.
# Omit this mapping to disable console logging.
# logging:
#   enabled: true
#   level: info # debug, info, warn, or error

# Optional LLM-call observability. The whole example remains commented, so
# model calls are not observed unless it is explicitly enabled.
# observability:
#   langfuse:
#     enabled: true
#     base_url: ${LANGFUSE_BASE_URL} # Defaults to https://cloud.langfuse.com
#     public_key: ${LANGFUSE_PUBLIC_KEY}
#     secret_key: ${LANGFUSE_SECRET_KEY}
#     environment: development
#     service_name: agentcli
#     release: ${APP_VERSION}
#     sample_rate: 1.0
#     capture:
#       input: true
#       output: true
#       reasoning: false

# Remove this mapping or set auto: false to disable new compactions.
compaction:
  auto: true
  provider: primary
  model: gpt-4.1-mini

providers:
  primary:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
    request_timeout: 2m
    models:
      gpt-4.1-mini:
        context_window_tokens: 122880
        max_output_tokens: 66560
        # reasoning: false # Optional Qwen-compatible shorthand.
        # extra_body: # Optional model-specific top-level request JSON.
        #   thinking:
        #     type: disabled
```

Provider names such as `primary` are aliases. The required `type` selects the
adapter; `openai` is currently supported.

Entries under `models` are optional exact-name overrides, not an allowlist.
`MAIN.md` and subagents remain free to select any model name; an unlisted model
simply uses provider discovery/default metadata and receives no request
override. Within a matching entry, `reasoning: false` is a Qwen-compatible
shorthand for `chat_template_kwargs.enable_thinking: false`. For other
OpenAI-compatible extensions, `extra_body` merges arbitrary top-level JSON
into every request for that exact model. For example, DeepSeek-compatible
gateways can use `extra_body: {thinking: {type: disabled}}`. Extra-body values
override standard request fields with the same names.

The optional `compaction` mapping only selects a separate summarizer through an
existing provider alias. Optional `context_window_tokens` and
`max_output_tokens` belong to each exact model entry, so main, subagent, and
summarizer models sharing one provider can still use independent limits. When
omitted, startup checks that profile's `/models` endpoint, then models.dev, and
finally defaults to 122,880 context tokens and 66,560 output tokens (`120k` and
`65k`). The output value remains model capability metadata; the operational
main-model output reserve defaults to one eighth of its context window, capped
at 16,384 tokens and reduced by lower model or request limits. Compaction
preserves the complete stored transcript and appends
internal checkpoints that resumed sessions keep projecting, even after
`auto: false` disables future compactions. See
the [project configuration guide](https://mrbryside.github.io/agentcli/getting-started/project-configuration/)
for lifecycle and fallback behavior.

Create `.agentcli/MAIN.md`:

```markdown
---
provider: primary
model: gpt-4.1-mini
---

Understand the requested result and provide a clear, self-contained answer.
```

Omit `tools` or `skills` when none are allowed. Project configuration may also
include `.agentcli/skill/*/SKILL.md` and `.agentcli/agent/*/*.md`.

## Subagent tasks

The main model has one `task` tool for focused subagent work. New tasks name an
agent and provide a short description and prompt; they run in the foreground by
default and return final text in the same main-agent turn. Submit independent
tasks in one tool-call batch for parallel execution. To continue a saved task,
call `task` with its `task_id` and a new prompt. Background tasks and promoted
foreground tasks are delivered by the runtime exactly once; terminal and HTTP
clients do not create a separate follow-up turn for their result. Host-facing
subagent session management remains available independently.

Application prompts own the domain decision: `MAIN.md` or a skill may require
a named agent, independent or sequential work, or continuation of the same
agent conversation. The framework explains the `task` fields that implement
those choices but does not decide whether a particular request must reuse an
earlier task. Subagent definition bodies describe the role, method, and quality
bar; the framework supplies task orchestration and child delivery contracts.

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
Set `Tool.RequiredSkills` when a handler must execute only after specific
skills have been loaded successfully in the current turn. AgentCLI adds the
requirement to the model-facing description and enforces it before admission
or handler execution, including when `load_skill` reports
`instructions_in_context=true`.

## Run the terminal playground

```go
err := agent.RunTerminal(
    agentcli.WithTerminalSessionID("manual-check"),
)
```

The included playground registers example `glob`, `read`, and confirmation
tools and enables debug runtime logging with `WithLogLevel`. In the interactive
Terminal, press `Ctrl+L` or enter `/logs` to inspect framework lifecycle
records:

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
