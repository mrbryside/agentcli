---
title: Your first agent
sidebar_position: 3
---

# Your first agent

This example loads a project, registers one explicit custom tool, starts a
session turn, and renders streaming text.

```go
package main

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "log"
    "strings"

    "github.com/mrbryside/agentcli"
    "github.com/mrbryside/agentcli/agentruntime"
    "github.com/mrbryside/agentcli/provider"
)

type lookupArguments struct {
    Topic *string `json:"topic"`
}

type lookupResult struct {
    Topic   string `json:"topic"`
    Summary string `json:"summary"`
}

func newLookupTool() agentcli.Tool {
    return agentcli.Tool{
        Definition: agentcli.ToolDefinition{
            Name:        "lookup_topic",
            Description: "Look up application-owned information about one topic.",
            InputSchema: agentcli.ObjectSchema(struct {
                Topic agentcli.ToolParameter
            }{
                Topic: agentcli.StringParameter("Topic to look up").
                    Required().
                    MinLength(1).
                    MaxLength(120),
            }),
        },
        Handler: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
            if err := ctx.Err(); err != nil {
                return nil, err
            }
            var input lookupArguments
            if err := agentcli.DecodeArguments(raw, &input); err != nil {
                return nil, err
            }
            if input.Topic == nil || strings.TrimSpace(*input.Topic) == "" {
                return nil, errors.New("topic is required")
            }
            topic := strings.TrimSpace(*input.Topic)
            return json.Marshal(lookupResult{
                Topic:   topic,
                Summary: "Application-owned information about " + topic,
            })
        },
    }
}

func main() {
    ctx := context.Background()
    project, err := agentcli.LoadProject(".")
    if err != nil {
        log.Fatal(err)
    }
    agent, err := agentcli.New(ctx,
        agentcli.WithProject(project),
        agentcli.WithTool(newLookupTool()),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer agent.Close()

    run, subscription, err := agent.StartSubscribed(ctx, agentruntime.Request{
        SessionID: "demo-session",
        Message: agentruntime.Message{
            Type:    agentruntime.MessageTypeUser,
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

`ObjectSchema` builds the provider contract. `DecodeArguments` accepts exactly
one JSON object and rejects unknown fields or trailing values. Pointer fields
distinguish a missing property from a Go zero value; the handler still owns
semantic validation and output encoding.

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
