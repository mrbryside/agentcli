---
title: Build an agentcli application
sidebar_position: 1
---

# Build an agentcli application

This example combines project configuration, a typed custom tool, dynamic
confirmation, permissions, streaming events, and the HTTP server.

## Files

```text
my-agent/
├── main.go
├── AGENTS.md
└── .agentcli/
    ├── config.yaml
    └── MAIN.md
```

## `.agentcli/config.yaml`

```yaml
permission_mode: default
providers:
  primary:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    request_timeout: 2m
```

## `.agentcli/MAIN.md`

```markdown
---
provider: primary
model: gpt-4.1-mini
tools:
  - publish_report
---

Help the user prepare reports. Call publish_report only when the user asks to
publish, and accurately report permission denials or declined confirmations.
```

## `AGENTS.md`

```markdown
# Application rules

- Never invent a publication destination.
- Summarize the final tool result in plain language.
```

## `main.go`

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "log"
    "os"
    "os/signal"
    "strings"
    "syscall"
    "time"

    "github.com/mrbryside/agentcli"
    "github.com/mrbryside/agentcli/confirmation"
    "github.com/mrbryside/agentcli/permission"
    "github.com/mrbryside/agentcli/toolexecution"
)

type publishInput struct {
    Title       string `json:"title" description:"Report title" minLength:"1" maxLength:"120"`
    Destination string `json:"destination" description:"Publication destination" minLength:"1" maxLength:"200"`
}

type publishOutput struct {
    Published   bool   `json:"published"`
    Destination string `json:"destination"`
}

type publisher struct{}

func (publisher) publish(ctx context.Context, input publishInput) (publishOutput, error) {
    if err := ctx.Err(); err != nil {
        return publishOutput{}, err
    }
    // Replace this deterministic example with an application API call.
    return publishOutput{Published: true, Destination: input.Destination}, nil
}

func withPublishTool(service publisher) agentcli.Option {
    return agentcli.WithCustomTool(
        "publish_report",
        "Publish a prepared report to an application destination.",
        service.publish,
        agentcli.StaticToolPermission(toolexecution.PermissionConfig{
            Actions: []permission.Action{permission.NetworkAccess},
            Risk:    permission.RiskHigh,
            Reason:  "Publishes data outside the local application.",
        }),
        agentcli.ToolConfirmation(func(input publishInput) (confirmation.Description, error) {
            destination := strings.TrimSpace(input.Destination)
            if destination == "" {
                return confirmation.Description{}, errors.New("destination is required")
            }
            return confirmation.Description{
                Title:   "Publish report",
                Message: "Publish this report now?",
                Details: fmt.Sprintf("Title: %s\nDestination: %s", input.Title, destination),
            }, nil
        }),
    )
}

func main() {
    root, err := os.Getwd()
    if err != nil {
        log.Fatal(err)
    }
    project, err := agentcli.LoadProject(root)
    if err != nil {
        log.Fatal(err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    agent, err := agentcli.New(ctx,
        agentcli.WithProject(project),
        withPublishTool(publisher{}),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer agent.Close()

    server, err := agentcli.NewServer(agent,
        agentcli.WithServerAddress("127.0.0.1:8080"),
        agentcli.WithServerHeartbeat(15*time.Second),
        agentcli.WithServerTurnQueueLimit(32),
    )
    if err != nil {
        log.Fatal(err)
    }

    go func() {
        <-ctx.Done()
        shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        _ = server.Shutdown(shutdown)
    }()

    if err := server.Run(); err != nil {
        log.Fatal(err)
    }
}
```

## Exercise the flow

Start the application:

```bash
export OPENAI_API_KEY='...'
go run .
```

Start a turn:

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/sessions/report-demo/turns \
  -H 'Content-Type: application/json' \
  -d '{"message":"Publish Quarterly review to the internal portal"}'
```

Open its `events_url`. In `default` mode, the high-risk network capability first
emits `permission_requested`. Post `allow_once`. The same run then emits
`confirmation_requested`; show the title, details, and message and post
`answer: yes`. After the handler completes, the stream contains
`tool_result_received` and the provider produces its final response.

Changing mode to `criticalOnly` still asks because the tool is high risk.
Changing to `unrestricted` skips the permission but still requires the Yes/No
confirmation.

## Add a custom frontend

The frontend needs these state stores:

- session and turn records;
- latest event cursor per turn;
- content fragments per visible turn;
- pending permission/confirmation records keyed by their ID;
- message transcripts fetched from `/messages`;
- current permission mode.

For a full chat UI, subscribe once to
`GET /v1/sessions/{sessionID}/events`. Its cursor spans all root turns and also
discovers parent callback turns created automatically when subagents finish.
Use a per-turn `events_url` only when the caller needs to follow one known
request in isolation.

When a second message is submitted during an active turn, store the returned
queued turn and its `queue_position`; do not retry the POST. Open its event
stream immediately or when it becomes visible—the server holds that stream
until FIFO admission. Use the existing interrupt endpoint to cancel a queued
message before execution.

Treat an SSE disconnect as a transport event, not a failed run. Reconnect with
the last durable sequence before deciding whether the run ended.

See [Build an application with the API](./api-client-integration.md) for the
complete browser flow, session activity envelope, decision handling, reload
recovery, and application use cases.
