---
title: Project Configuration
sidebar_position: 2
---

# Project Configuration

`agentcli.LoadProject(root)` reads an immutable project snapshot from
`.agentcli/`:

```text
.agentcli/
├── config.yaml              # required
├── MAIN.md                  # required
├── skill/*/SKILL.md         # optional
└── agent/*/*.md             # optional
```

`Project` is intentionally opaque. Load it and pass it to
`agentcli.WithProject(project)`; provider construction, prompts, skills, and
full task-agent definitions remain framework internals.

Unknown fields, missing providers, unknown skills, and unregistered tools fail
during startup rather than during a model request.

## Provider settings

Start with one OpenAI-compatible provider:

```yaml title=".agentcli/config.yaml"
permission_mode: default

providers:
  primary:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
    request_timeout: 2m
```

Provider names such as `primary` are local aliases. Environment references use
`${NAME}` and missing variables are load errors. `openai` is currently the
supported provider type; any OpenAI-compatible chat-completions endpoint can
use it.

Entries under `models` are exact-name overrides, not an allowlist. A model not
listed there can still be selected by `MAIN.md` or a task agent.

## Model-specific overrides

One exact model entry may contain capability metadata and provider-specific
request fields together:

```yaml title=".agentcli/config.yaml"
providers:
  primary:
    type: openai
    url: https://your-provider.example/v1
    api_key: ${API_KEY}
    models:
      replace-model:
        context_window_tokens: 122880
        max_output_tokens: 66560
        extra_body:
          thinking:
            type: disabled
```

The key `replace-model` must exactly match the model selected in `MAIN.md`, a
task-agent definition, or `compaction.model`. Overrides do not affect other
models using the same provider profile.

### Context and output limits

| Field | What AgentCLI uses it for |
| --- | --- |
| `context_window_tokens` | The model's total context capacity. AgentCLI estimates whether the system prompts, transcript, tool schemas, requested output reserve, and current input fit before deciding to compact. |
| `max_output_tokens` | The model's maximum generation capability. It bounds the output reserve and compaction-summary budget; it does not force every response to generate that many tokens. |

Use limits published for the exact model and endpoint. If configured manually,
`context_window_tokens` must be positive; `max_output_tokens` may be omitted,
and zero means that no reliable output limit is known. A positive output limit
cannot be larger than the context window. Do not configure only
`max_output_tokens`, because AgentCLI cannot budget a request without a context
window.

AgentCLI normally reserves a smaller operational output budget rather than the
entire advertised maximum. That budget is bounded by 16,384 tokens, one eighth
of the context window, the model output limit, and any lower per-request limit.
This leaves room for input and avoids compacting small-context models too early.

Manual limits take priority and avoid metadata discovery for that exact model.
When they are absent, project loading tries these sources in order:

1. the authenticated provider `<url>/models` endpoint;
2. the public `models.dev` catalog;
3. deterministic fallbacks of 122,880 context tokens and 66,560 output tokens.

Resolution happens independently for the main model, each task-agent model,
and the optional compaction model, so models sharing one provider can still
have different limits.

### Reasoning and `extra_body`

`reasoning: false` is a Qwen-compatible shorthand. AgentCLI converts it to:

```json
{"chat_template_kwargs":{"enable_thinking":false}}
```

DeepSeek-compatible gateways commonly use a different top-level request field:

```yaml
models:
  replace-model:
    extra_body:
      thinking:
        type: disabled
```

`extra_body` accepts YAML values representable as JSON and merges them into the
top level of every chat-completions request for that exact model. Environment
references such as `${THINKING_TYPE}` are expanded recursively. An
`extra_body` key overrides a standard request field with the same name, so add
only fields documented by the selected endpoint. Remove the `thinking` mapping
when the endpoint does not accept it.

Token metadata and `extra_body` are independent: the former controls local
budgeting and compaction, while the latter changes the JSON sent to the
provider.

## Main agent

```md title=".agentcli/MAIN.md"
---
provider: primary
model: your-model
tools:
  - glob
  - read
skills:
  - code-review
---

You are a coding assistant. Inspect the project before proposing changes.
```

The body contains product instructions. The frontmatter selects the provider,
model, and allowlisted tools and skills. Task-agent definitions are discovered
from `.agentcli/agent/` and exposed through the framework-owned `task` tool;
they are not listed in `MAIN.md`.

Optional runtime logging may be enabled here:

```yaml title=".agentcli/config.yaml"
logging:
  enabled: true
  level: info
```

`agentcli.WithLogLevel(...)` is the programmatic alternative and overrides the
project logger when applied after `agentcli.WithProject(project)`. Langfuse is
code-configured only through `agentcli.WithLangfuse(...)`; an `observability`
YAML field is rejected. See
[Logging and observability](../observability/overview.md). Optional transcript
compaction remains a project setting; see
[Runs, sessions, and events](../agentcli/runs-and-sessions.md#context-compaction).

## Permission modes

`permission_mode` controls declared tool permissions:

| Mode | Behavior |
| --- | --- |
| `default` | Ask when no stored decision or matching policy exists. |
| `acceptEdits` | Allow filesystem-write-only calls; ask for others. |
| `criticalOnly` | Allow low/medium risk; ask for high risk. |
| `dontAsk` | Deny calls that would require a question. |
| `plan` | Deny executable capabilities while planning. |
| `unrestricted` | Allow declared permissions unless a rule asks or denies. |

Confirmations remain separate Yes/No decisions and are never bypassed by a
permission mode. See [Safety, permissions, and confirmations](../tools/permissions-and-confirmations.md).

Programmatic options passed after `agentcli.WithProject(project)` can override
project defaults such as the model, logger, storage, permission policy, and
provider-step limit. The
[Go API](https://pkg.go.dev/github.com/mrbryside/agentcli) lists those options.
