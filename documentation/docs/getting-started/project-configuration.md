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
    models:
      your-model:
        context_window_tokens: 122880
        max_output_tokens: 66560
```

Provider names such as `primary` are local aliases. Environment references use
`${NAME}` and missing variables are load errors. `openai` is currently the
supported provider type; any OpenAI-compatible chat-completions endpoint can
use it.

Entries under `models` are exact-name overrides, not an allowlist. They may
also set `reasoning` for Qwen-compatible endpoints or `extra_body` for
provider-specific request fields.

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
