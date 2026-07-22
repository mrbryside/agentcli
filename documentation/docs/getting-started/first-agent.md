---
title: Your first agent
sidebar_position: 3
---

# Your first agent

This example loads a project, registers one typed custom tool, starts a session
turn, and renders streaming text.

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/mrbryside/agentcli"
    "github.com/mrbryside/agentcli/agentruntime"
    "github.com/mrbryside/agentcli/provider"
)

type lookupInput struct {
    Topic string `json:"topic" description:"Topic to look up" minLength:"1"`
}

type lookupOutput struct {
    Topic   string `json:"topic"`
    Summary string `json:"summary"`
}

func withLookupTool() agentcli.Option {
    return agentcli.WithCustomTool(
        "lookup_topic",
        "Return a deterministic local description of a topic.",
        func(ctx context.Context, input lookupInput) (lookupOutput, error) {
            if err := ctx.Err(); err != nil {
                return lookupOutput{}, err
            }
            return lookupOutput{
                Topic: input.Topic,
                Summary: "Application-owned information about " + input.Topic,
            }, nil
        },
    )
}

func main() {
    ctx := context.Background()
    project, err := agentcli.LoadProject(".")
    if err != nil {
        log.Fatal(err)
    }
    agent, err := agentcli.New(ctx,
        agentcli.WithProject(project),
        withLookupTool(),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer agent.Close()

    run, subscription, err := agent.StartSubscribed(ctx, agentruntime.Request{
        SessionID: "demo-session",
        Message: agentruntime.Message{
            Type: agentruntime.MessageTypeUser,
            Content: "Use lookup_topic for Go and summarize the result.",
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    for event := range subscription.Events {
        if event.Type == agentruntime.ProviderEventReceived &&
            event.ProviderEvent.Type == provider.ContentReceived {
            fmt.Print(event.ProviderEvent.Content)
        }
    }
    if _, err := run.Result(); err != nil {
        log.Fatal(err)
    }
}
```

Add `lookup_topic` to `.agentcli/MAIN.md`:

```yaml
tools:
  - lookup_topic
```

## Why `StartSubscribed`?

`StartSubscribed` installs a live subscription before `RunStarted` is
published, so a newly created UI cannot miss the beginning of the turn. Calling
`Start` and then `run.Subscribe` is valid for consumers that intentionally do
not need those earliest live events.

## Continue the session

Start another turn with the same `SessionID` and a new user message. The runtime
loads the stored user, assistant, tool-call, and tool-result history and
transforms it to provider messages at the provider boundary.

Only one active turn may own a session. Concurrent work should use distinct
session IDs.

