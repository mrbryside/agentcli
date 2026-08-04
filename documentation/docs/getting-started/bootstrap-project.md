---
title: Bootstrap a project
sidebar_position: 2
---

# Bootstrap a project

Create a minimal terminal-agent project:

```sh
curl -fsSL https://raw.githubusercontent.com/mrbryside/agentcli/main/init/install.sh | sh
```

The installer asks for a new project folder and a Go module path. It refuses to
overwrite an existing path and never asks for or stores provider credentials.

## Generated project

```text
my-agent/
├── go.mod
├── main.go
├── tool_glob.go
├── tool_read.go
└── .agentcli/
    ├── config.yaml
    └── MAIN.md
```

The starter is intentionally read-only. The application registers only
`glob` and `read`, and `MAIN.md` allowlists those same tools:

```md
---
provider: replace-provider
model: replace-model
tools:
  - glob
  - read
---

You are chatbot.
```

There are no generated guardrails, edit/report tools, skills, task agents,
runtime logging settings, or observability settings.

## Configure the provider

The generated `.agentcli/config.yaml` contains one provider profile and an
enabled compaction mapping:

```yaml
permission_mode: criticalOnly

compaction:
  auto: true
  provider: replace-provider
  model: replace-model

providers:
  replace-provider:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
    request_timeout: 2m
    models:
      replace-model:
        context_window_tokens: 122880
        max_output_tokens: 66560
```

Replace `replace-provider` and `replace-model` consistently in `config.yaml`
and `MAIN.md`. Remove the `compaction` mapping if automatic transcript
compaction is not wanted. Keep the API key in the process environment:

```sh
cd my-agent
export API_KEY='replace-with-a-real-key'
go run .
```

## Starter tools

- `glob` searches below the project root, supports recursive `**`, excludes
  protected paths, defaults to 100 matches, and returns at most 500.
- `read` returns bounded UTF-8 project text, excludes protected paths, and
  provides `next_offset` when more content remains.

Both tools declare low-risk filesystem-read permission and use strict public
`agentcli` schemas and argument decoding.

Continue with [Project configuration](project-configuration.md) for provider,
agent, compaction, skill, and tool allowlist details.
