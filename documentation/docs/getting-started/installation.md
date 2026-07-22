---
slug: /
title: Installation
sidebar_position: 1
---

# Installation

## Requirements

- Go `1.26.3` or newer for the current module.
- Node.js `20` or newer only when running this documentation site.
- An OpenAI-compatible chat-completions endpoint and API key for live model
  examples.

## Use the Go module

Inside this repository, package imports use the module path `harness-api`:

```go
import (
    "harness-api/agentcli"
    "harness-api/agentruntime"
)
```

Download dependencies and run the tests:

```bash
go mod download
go test ./...
```

## Configure a live provider

Copy the safe template and export the referenced key:

```bash
cp .agentcli/config.example.yaml .agentcli/config.yaml
export OPENAI_API_KEY='replace-with-a-real-key'
```

Do not replace `${OPENAI_API_KEY}` with a committed secret. Project loading
expands environment-variable references but intentionally does not load a
`.env` file.

Run the interactive terminal playground:

```bash
go run ./playground/terminal
```

Run a one-shot prompt:

```bash
go run ./playground/terminal "Explain the agent event lifecycle"
```

One-shot mode is non-interactive: pending permissions are denied and Yes/No
confirmations are declined rather than bypassed.

## Run the examples

Each directory below is an independent `package main`:

```bash
go run ./agentcli/example/basic
go run ./agentcli/example/customtool
go run ./agentcli/example/server
```

Examples requiring a provider read their `.agentcli` project through the shared
example loader. The reconnect and permission-mode examples use local test-style
models and need no external API key.

## Run this documentation site

```bash
cd documentation
npm install
npm start
```

Create a production build:

```bash
npm run build
npm run serve
```

The static output is written to `documentation/build/`.
