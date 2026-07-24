---
title: Custom tools
sidebar_position: 1
---

# Custom tools

Application tools are explicitly registered Go functions. Skills cannot create
handlers, and project Markdown cannot enable an unregistered executable
capability.

## Low-level raw-handler API

Use `WithTool` when you want to own JSON decoding but still want a typed,
described schema. The public facade keeps this declaration in the `agentcli`
package; the handler remains the familiar raw JSON handler.

```go
type readParameters struct {
    Path   agentcli.ToolParameter
    Offset agentcli.ToolParameter
    Limit  agentcli.ToolParameter
}

readTool := agentcli.Tool{
    Definition: agentcli.ToolDefinition{
        Name: "read",
        Description: "Read a project text file.",
        InputSchema: agentcli.ObjectSchema(readParameters{
            Path: agentcli.StringParameter("Project-relative path").Required().MinLength(1),
            Offset: agentcli.IntegerParameter("First 1-based line").Minimum(1),
            Limit: agentcli.IntegerParameter("Maximum lines").Minimum(1).Maximum(2000),
        }),
    },
    Handler: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
        // Decode and execute the application-specific operation.
    },
    Permission: agentcli.ToolStaticPermission(agentcli.ToolPermissionConfig{
        Actions: []agentcli.PermissionAction{agentcli.FilesystemRead},
        Risk: agentcli.RiskLow,
    }),
}

agent, err := agentcli.New(ctx, agentcli.WithTool(readTool))
```

`ToolParameter` carries the field description and required marker. Its
`Schema` field accepts any `InputSchema` for advanced declarations; `Parameter`
attaches a description to an existing schema. `ObjectSchema` uses `json` tags
when supplied and otherwise turns exported field names into `lower_snake_case`.
Use `TryObjectSchema` if a dynamically assembled declaration should return an
error instead of panicking during initialization.

## Handler context

Every tool handler receives a context containing the current invocation
metadata. Read it through the public facade:

```go
invocation, ok := agentcli.ToolInvocationFromContext(ctx)
if !ok {
    return nil, errors.New("tool invocation context is unavailable")
}

fmt.Println(invocation.SessionID)
fmt.Println(invocation.TurnID)
fmt.Println(invocation.CallID)
fmt.Println(invocation.ToolName)
```

`agentcli.ToolInvocation` contains:

| Field | Meaning |
| --- | --- |
| `SessionID` | Session that owns the current run. |
| `TurnID` | Current user/model turn. |
| `CallID` | Unique provider tool-call ID for this invocation. |
| `ToolName` | Registered name of the executing tool. |

The executor attaches this context after admission and before invoking the
handler. Treat these values as metadata, not user input. `WithToolInvocation`
is available for direct handler tests and adapters; normal applications do not
need to attach it themselves.

The executor also attaches the immutable permission-policy snapshot used for
admission:

```go
policy, ok := agentcli.ToolPermissionPolicyFromContext(ctx)
```

Use this only for policy-aware classification or diagnostics. Do not mutate it
or use it as a replacement for authorization checks. Cancellation and deadline
signals remain available through the standard `ctx.Done()` and `ctx.Err()` APIs.

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

For a tool that must finalize every turn, use
`agentcli.ToolRequiredAtTurnEnd()`. It also applies `EndTurn`. If the model
tries to finish without a successful call, the runtime performs one repair
provider round exposing only the missing finalizer tools. Every missing
finalizer must be called in that response; a second omission or failure fails
the turn.

```go
agentcli.WithCustomTool(
    "save_turn_summary",
    "Persist the final structured summary for this turn.",
    saveTurnSummary,
    agentcli.ToolRequiredAtTurnEnd(),
)
```

The runtime always waits for every result in the current parallel tool-call
batch before deciding. It completes the turn only when every result succeeded
and every tool in that batch uses `EndTurn`. A `ContinueTurn` result keeps the
loop open, and failed, interrupted, denied, or declined results continue to the
provider so it can explain or recover from the error.

Framework tools may represent an expected admission conflict as a successful
structured result when provider recovery would be harmful. For example,
`send_subagent_message` returns `action: callback_pending` and `accepted: false`
instead of a failed result when the child's authoritative callback is already
waiting. Its resolved `finish_turn` still controls `EndTurn` or `ContinueTurn`;
real validation, ownership, storage, and execution failures remain failed tool
results.

`start_subagent`, `send_subagent_message`, and `force_close_subagent` expose a
model-facing `finish_turn` argument. It defaults to `true`, applying `EndTurn`
after a final dispatch or force-close. The model sets it to `false` only while
it has a concrete plan to continue decomposing work or issue more operations
after the current tool batch. It must set `true` on the final operation, when
no more subagent operations are planned, or when unsure. A
`selection_required` start always continues because no child work was
dispatched and the model must ask which existing child the user means. Their
model-facing tool results echo the resolved `finish_turn`
boolean and a `turn_behavior` label of `continue_turn` or `end_turn`, plus an
instruction matching that decision. `close_subagent` has no `finish_turn`
argument; it always returns `continue_turn` so the callback can be delivered in
the next normal provider round.

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

`WithTool(agentcli.Tool{...})` remains available for handlers that already
operate on raw JSON or require an unusual schema. Use `RawInputSchema` only as
an explicit escape hatch:

```go
schema, err := agentcli.RawInputSchema(json.RawMessage(`{"type":"object","x-vendor-rule":true}`))
if err != nil {
    return err
}

agentcli.WithTool(agentcli.Tool{
    Definition: agentcli.ToolDefinition{
        Name:        "advanced_tool",
        Description: "An application-defined advanced tool.",
        InputSchema: schema,
    },
    Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
        return json.RawMessage(`{"ok":true}`), nil
    },
    TurnBehavior: agentcli.EndTurn,
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
