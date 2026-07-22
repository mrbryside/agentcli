---
title: Custom tools
sidebar_position: 1
---

# Custom tools

Application tools are explicitly registered Go functions. Skills cannot create
handlers, and project Markdown cannot enable an unregistered executable
capability.

## Recommended typed API

`WithCustomTool` removes raw schema, decode, and encode boilerplate:

```go
type weatherInput struct {
    City string `json:"city" description:"City and country" minLength:"1" maxLength:"120"`
    Unit string `json:"unit,omitempty" description:"Temperature unit" enum:"celsius,fahrenheit"`
}

type weatherOutput struct {
    City        string  `json:"city"`
    Temperature float64 `json:"temperature"`
    Unit        string  `json:"unit"`
}

func withWeatherTool(client *WeatherClient) agentcli.Option {
    return agentcli.WithCustomTool(
        "get_weather",
        "Get current weather for a city.",
        func(ctx context.Context, input weatherInput) (weatherOutput, error) {
            return client.Current(ctx, input.City, input.Unit)
        },
        agentcli.StaticToolPermission(toolexecution.PermissionConfig{
            Actions: []permission.Action{permission.NetworkAccess},
            Risk:    permission.RiskMedium,
            Reason:  "Fetches live weather from the configured weather service.",
        }),
    )
}
```

Register it during initialization:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    withWeatherTool(weatherClient),
)
```

The framework:

1. infers an object JSON Schema from `weatherInput`;
2. rejects malformed JSON and unknown fields;
3. decodes arguments into `weatherInput`;
4. calls the typed handler with the turn-scoped context;
5. encodes `weatherOutput` as the tool result;
6. returns handler errors as failed tool results so the model can respond.

## Continue or end the turn

Tools continue the normal agent loop by default: after a successful result is
stored, the provider receives the updated transcript and may produce text or
request more tools.

For asynchronous dispatch tools, the follow-up provider step can be wrong. It
may speculate about work that has only been queued or duplicate a later
callback. Configure `EndTurn` to store the successful result and complete the
current turn without another provider call:

```go
agentcli.WithCustomTool(
    "enqueue_report",
    "Queue a report for asynchronous generation.",
    enqueueReport,
    agentcli.ToolTurnBehavior(agentcli.EndTurn),
)
```

The available values are:

| Behavior | Result |
| --- | --- |
| `agentcli.ContinueTurn` | Default. Store the result and call the provider again. |
| `agentcli.EndTurn` | Store a successful result and complete the turn immediately. |

The runtime always waits for every result in the current parallel tool-call
batch before deciding. If any successful tool in that batch uses `EndTurn`, it
stores the entire ordered batch and completes the turn. Failed, interrupted,
denied, or declined results continue to the provider so it can explain or
recover from the error.

`start_subagent` and `send_subagent_message` use `EndTurn` because their
successful results confirm asynchronous dispatch, not child completion. The
authoritative child answer arrives later through a callback turn. If
`start_subagent` returns `selection_required`, it temporarily continues the
turn because no child work was dispatched and the model must ask which existing
child the user means.

## Dynamic permission metadata

Use typed arguments when the capability details depend on the request:

```go
agentcli.ToolPermission(func(input writeInput) (permission.Description, error) {
    return permission.Description{
        Actions: []permission.Action{permission.FilesystemWrite},
        Risk:    permission.RiskMedium,
        Reason:  "Writes the user-selected report.",
        Details: "Path: " + input.Path,
    }, nil
})
```

For policy-aware classification, use `ToolPermissionWithPolicy`. The policy is
an immutable snapshot captured when the tool request enters execution.

## Yes/No confirmation

Confirmation is independent from permission:

```go
agentcli.ToolConfirmation(func(input publishInput) (confirmation.Description, error) {
    return confirmation.Description{
        Title:   "Publish report",
        Message: "Publish this report now?",
        Details: "Destination: " + input.Destination,
    }, nil
})
```

If both permission and confirmation are configured, permission admission runs
first. Confirmation appears immediately before execution. The handler receives
the same strictly decoded input after a correlated Yes decision.

## Build a tool before agent options

Use `NewCustomTool` when another component needs the low-level value:

```go
tool, err := agentcli.NewCustomTool(
    "lookup_topic",
    "Look up a topic.",
    lookupTopic,
)
if err != nil {
    return err
}

agent, err := agentcli.New(ctx, agentcli.WithTool(tool))
```

`NewCustomTool` and `WithCustomTool` have the same generic inference and
functional options. The latter reports construction errors from `agentcli.New`
with the option index.

## Advanced raw API

`WithTool(toolexecution.Tool{...})` remains available for handlers that already
operate on raw JSON or require an unusual schema:

```go
agentcli.WithTool(toolexecution.Tool{
    Definition: agentruntime.ToolDefinition{
        Name:        "advanced_tool",
        Description: "An application-defined advanced tool.",
        InputSchema: json.RawMessage(`{"type":"object"}`),
    },
    Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
        return json.RawMessage(`{"ok":true}`), nil
    },
    TurnBehavior: toolexecution.EndTurn,
})
```

Use this only when typed construction is insufficient. Raw handlers own strict
decoding, validation, result encoding, and safe display of argument-derived
metadata.

## Tool allowlists

Registration and model availability are separate:

- Go registration gives the executor a handler.
- `MAIN.md` `tools` decides which registered tools the root model sees.
- A subagent definition's `tools` decides which registered tools that child
  sees.

Subagents never receive root-only framework management tools, so they cannot
spawn nested subagents.

## Handler rules

- Honor cancellation through `ctx`.
- Validate semantic constraints; JSON Schema primarily guides and constrains
  model-generated arguments.
- Return structured output instead of prose when possible.
- Never trust paths, URLs, or commands simply because a model supplied them.
- Avoid including secrets in tool output, errors, permission details, or
  confirmation details because those values may be stored or shown to users.
