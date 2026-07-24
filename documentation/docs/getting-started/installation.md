---
slug: /
title: Installation
sidebar_position: 1
---

# Installation

## Requirements

- Go `1.26.3` or newer for the current module.
- An OpenAI-compatible chat-completions endpoint and API key for live model
  runs.

## Use the Go module

Add the package to your application:

```bash
go get github.com/mrbryside/agentcli
```

Import the root package directly:

```go
import (
    "github.com/mrbryside/agentcli"
    "github.com/mrbryside/agentcli/agentruntime"
)
```

Download dependencies and run the tests:

```bash
go mod download
go test ./...
```

## Generate a terminal project

For a complete starter application instead of adding the library to an
existing module, run:

```bash
curl -fsSL https://raw.githubusercontent.com/mrbryside/agentcli/main/init/install.sh | sh
```

The installer prompts only for the target folder and Go module path. It never
requests or persists provider credentials. It creates a Terminal application,
project configuration, example skill, researcher subagent, and bounded
`read`/`glob`/`edit` tools. The generated `edit` performs one exact unique
replacement and requires both write permission and confirmation. The main
agent also receives a network-free `report_discord` mock that is required once
as the standalone final action of each turn. The agent omits `skipReport` or
sets it to `false` to record the final message, and may set it to `true` after
deciding the turn has no useful user-facing content worth reporting. Its
prompt output guard returns a failed tool result with repair feedback when the
final payload is non-compliant. See
[Bootstrap a project](bootstrap-project.md) for the generated layout and the
required `replace-provider` and `replace-model` substitutions.

## Configure a live provider

For this repository playground or an existing module, copy the example once.
Generated projects already contain the destination file:

```bash
cp .agentcli/config.example.yaml .agentcli/config.yaml
export API_KEY='replace-with-a-real-key'
```

Keep the environment reference in YAML instead of committing a secret.
Project loading expands environment-variable references but intentionally does
not load or ask for a `.env` file.

Run the interactive terminal playground:

```bash
go run ./playground/terminal
```

Run a one-shot prompt:

```bash
go run ./playground/terminal "Explain the agent event lifecycle"
```

One-shot mode is non-interactive: pending permissions are denied and Yes/No
confirmations are declined rather than bypassed. This is an execution flag,
not another permission mode; see [Non-interactive execution](../tools/permissions-and-confirmations.md#non-interactive-execution).
