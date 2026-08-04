---
slug: /
title: Getting Started
sidebar_position: 1
---

# Getting Started

AgentCLI is a Go library for building agents with streaming, tools, safety
checks, task agents, a Terminal UI, and HTTP/SSE APIs.

You need Go `1.26.3` or newer and an OpenAI-compatible chat-completions
endpoint.

## Create a starter project

```sh
curl -fsSL https://raw.githubusercontent.com/mrbryside/agentcli/main/init/install.sh | sh
```

The installer asks for a new folder and Go module path. It creates a small,
read-only terminal agent:

```text
my-agent/
├── go.mod
├── main.go
├── tool_glob.go
├── tool_read.go
└── .agentcli/
    ├── config.yaml
    ├── MAIN.md
    ├── skill/
    │   └── interview/
    │       └── SKILL.md
    └── agent/
        └── researcher/
            └── researcher.md
```

It refuses to overwrite an existing path and never asks for or stores provider
credentials.

## Configure and run

Replace `replace-provider` and `replace-model` in both
`.agentcli/config.yaml` and `.agentcli/MAIN.md`, then provide the API key
through the environment:

```sh
cd my-agent
export API_KEY='your-api-key'
go run .
```

Comments in the generated config explain permissions, compaction, provider
aliases, model token limits, and the DeepSeek-compatible
`extra_body.thinking.type: disabled` field. Replace the example limits with the
values published by your endpoint, and remove or replace `extra_body` when the
endpoint does not accept it.

Run one non-interactive prompt by passing it as an argument:

```sh
go run . "Summarize this project"
```

The starter exposes only bounded `glob` and `read` tools. It also includes an
`interview` skill and a read-only `researcher` task agent. Their descriptions
show the main model when to load the skill or spawn the task agent, so the user
does not need to name either capability explicitly. See
[Skills and Task Agents](../capabilities/skills-and-task-agents.md).

## Add AgentCLI to an existing app

```sh
go get github.com/mrbryside/agentcli
```

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

Next, read [Project configuration](project-configuration.md), then add
application-owned behavior with [Custom tools](../tools/custom-tools.md).
