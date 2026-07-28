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
only the network-free `report_discord` trigger tool with a pre-execution prompt
tool-call guard; the researcher stays read-only with bounded `glob` and `read`.
The generated application also registers double-gated exact-match `edit`, but
neither generated agent exposes it by default. `read` returns at most 2,000
lines and a `next_offset` when more content remains. Tool source is generated
separately as `tool_read.go`, `tool_glob.go`, `tool_edit.go`, and
`tool_report_discord.go`. A trigger call made as the model's first provider
action is skipped with a successful continue result and must not be retried by
the model. After the remaining work and accepted subagent results or
follow-ups finish, completion repair requests the executable final call.
The runtime also accepts a later provider-round call when the complete response
scope is ready to end as a compatibility path,
but the agent decides whether a report is useful: omitting
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
mode, cap each main agent at four open subagents, and read provider credentials
only from the process environment. The generated config includes disabled,
commented examples for Langfuse observability and an OpenRouter-compatible
provider. When Go is available, the installer also runs `go mod tidy` so the
project can start immediately.

```sh
curl -fsSL https://raw.githubusercontent.com/mrbryside/agentcli/main/init/install.sh | sh
```

Then replace the generated provider/model placeholders, set the API key, and
start the app. Go is only needed at this point:

```sh
cd my-agent
export API_KEY='replace-with-a-real-key'
export GUARDRAILS_API_KEY='replace-with-a-real-guard-key'
go run .
```

## Project configuration

Create `.agentcli/config.yaml`:

```yaml
permission_mode: default
max_subagents: 4

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
tools. Its `.agentcli/config.example.yaml` also shows the current runtime
logging, compaction, model metadata, OpenRouter-compatible provider, and
disabled Langfuse observability settings. Uncomment `logging` and select
`debug`, `info`, `warn`, or `error` to inspect framework lifecycle records on
stderr:

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
