---
title: Custom tools
sidebar_position: 1
---

# Custom tools

`WithTool` is the application-tool registration API. A tool explicitly owns
its model-facing definition, raw JSON handler, turn behavior, finalizer flag,
and optional permission and confirmation descriptors.

## Define a tool

```go
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
        Handler: lookupTopic,
    }
}

func lookupTopic(
    ctx context.Context,
    raw json.RawMessage,
) (json.RawMessage, error) {
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
    if len(topic) > 120 {
        return nil, errors.New("topic must be at most 120 bytes")
    }
    return json.Marshal(lookupResult{
        Topic:   topic,
        Summary: "Application-owned information about " + topic,
    })
}
```

The schema is the provider contract. `DecodeArguments` is the strict runtime
shape boundary. Pointer fields distinguish missing values from zero values,
while the handler still validates business and security rules and marshals its
own result.

Register the tool and allowlist the same name in project Markdown:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithTool(newLookupTool()),
)
```

```yaml
tools:
  - lookup_topic
```

See [Input schemas](./input-schemas.md) for every parameter helper,
constraint, advanced `InputSchema`, and the `RawInputSchema` escape hatch.

## Handler context

Admitted handlers receive correlated invocation metadata:

```go
invocation, ok := agentcli.ToolInvocationFromContext(ctx)
```

`ToolInvocation` contains `SessionID`, `TurnID`, `CallID`, and `ToolName`.
`WithToolInvocation` exists for direct handler tests and adapters. The immutable
admission policy is available through `ToolPermissionPolicyFromContext`.
Metadata and policy are not user input or substitutes for authorization.

## Continue or end the turn

`ContinueTurn` is the zero-value default. After success, the provider sees the
stored result and continues:

```go
TurnBehavior: agentcli.ContinueTurn,
```

`EndTurn` permits completion without another provider request:

```go
TurnBehavior: agentcli.EndTurn,
```

The runtime waits for every call in a parallel batch. It ends the turn only
when all results succeeded and all called tools resolve to `EndTurn`. A mixed,
failed, denied, declined, or interrupted batch continues.

## Required end-of-turn tools

Set both fields for a tool that must finalize every turn in which it is
exposed:

```go
agentcli.Tool{
    Definition:        definition,
    Handler:           handler,
    TurnBehavior:      agentcli.EndTurn,
    RequiredAtTurnEnd: true,
}
```

The registry rejects a required finalizer without static `EndTurn`. Only a
successful terminal all-`EndTurn` batch satisfies completion; an earlier call
or a mixed continuing batch does not. While a finalizer is missing, normal
rounds request a tool without hiding domain tools. If the model still finishes,
the completion guard restricts repair rounds to missing finalizers and uses a
specific one-shot tool choice when only one remains. It permits up to three
consecutive no-progress repairs; progress resets that budget.

Tool choice is provider request policy, not direct execution. A provider that
ignores it can still exhaust the bounded guard and fail the turn. Required
finalizers should therefore be described as standalone final actions.

Several finalizers are supported. The terminal batch must successfully include
every still-missing finalizer and contain no continuing tool.

## Permissions and confirmations

A static capability declaration uses the root facade:

```go
Permission: agentcli.ToolStaticPermission(agentcli.ToolPermissionConfig{
    Actions: []agentcli.PermissionAction{agentcli.FilesystemRead},
    Risk:    agentcli.RiskLow,
    Reason:  "Reads one bounded project text file.",
}),
```

`Permission`, `PermissionWithPolicy`, and `Confirmation` fields accept raw JSON
descriptors. Decode and normalize their arguments before constructing
user-visible details. When both gates exist, permission is resolved first and
confirmation is published immediately before handler execution.

See [Permissions and confirmations](./permissions-and-confirmations.md) for
complete dynamic examples and mode behavior.

## Project allowlists

- `WithTool` registers the handler globally.
- `MAIN.md` selects which registered application tools the root sees.
- A subagent definition selects which registered application tools that child
  sees.

Required finalizer behavior applies only to agents that expose that tool.
Subagents never receive root-only management tools and cannot nest.

## Handler checklist

- Honor cancellation through `ctx`.
- Treat model arguments as untrusted.
- Use `DecodeArguments`, then validate semantics and target state.
- Bound I/O, execution time, and output.
- Normalize argument-derived permission/confirmation text.
- Return structured JSON where practical.
- Do not treat schemas, prompts, permissions, or confirmations as a sandbox.
