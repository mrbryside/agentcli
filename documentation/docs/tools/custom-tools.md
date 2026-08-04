---
title: Custom Tools
sidebar_position: 1
---

# Custom Tools

Application tools explicitly own their provider schema, handler, safety gates,
and turn behavior. AgentCLI does not infer tool schemas from Go structs.

## Define and register a tool

```go
type lookupArguments struct {
    Topic *string `json:"topic"`
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
            var input lookupArguments
            if err := agentcli.DecodeArguments(raw, &input); err != nil {
                return nil, err
            }
            if input.Topic == nil || strings.TrimSpace(*input.Topic) == "" {
                return nil, errors.New("topic is required")
            }
            return json.Marshal(map[string]string{
                "topic":   strings.TrimSpace(*input.Topic),
                "summary": "application-owned result",
            })
        },
    }
}
```

Register the handler in Go and allowlist its exact name in `MAIN.md` or a task
agent definition:

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

## Schemas and decoding

`ObjectSchema` creates a closed JSON object. Parameters are optional unless
`.Required()` is used.

| Helper | JSON type |
| --- | --- |
| `StringParameter` | `string` |
| `IntegerParameter` | `integer` |
| `NumberParameter` | `number` |
| `BooleanParameter` | `boolean` |
| `ArrayParameter` | `array` |
| `ObjectParameter` | `object` |

Common constraints include `.MinLength()`, `.MaxLength()`, `.Pattern()`,
`.Format()`, `.Minimum()`, `.Maximum()`, and `.Items()`. Use `InputSchema` for
advanced composition, or `RawInputSchema` for a validated raw object schema.

`DecodeArguments` rejects non-object JSON, unknown fields, trailing values,
and malformed input. It does not replace business validation, authorization,
path checks, or size limits. Pointer fields help distinguish missing values
from Go zero values.

## Handler context and skills

Handlers should honor cancellation through `ctx`. Correlation metadata is
available through:

```go
invocation, ok := agentcli.ToolInvocationFromContext(ctx)
```

`ToolInvocation` contains session, turn, call, and tool IDs. It is trusted
metadata, not user input.

Require workflow instructions before execution when needed:

```go
RequiredSkills: []string{"web-research"},
```

The named skills must be allowed for that agent and loaded in the current turn.
A missing load returns a non-executing result so the model can load the skill
and retry. Loading a skill does not register or unlock a tool.

## Turn and response-scope behavior

| Setting | Behavior |
| --- | --- |
| default | Execute now and continue the model loop. |
| `EndTurnOnSuccess: true` | End after the full parallel batch succeeds. |
| `Trigger: agentcli.EndTurn` | Require a successful call before the turn completes. |
| `Trigger: agentcli.EndResponseScope` | Require one final action after task results and follow-ups settle. |
| `ResponseScopeCallLimit: n` | Bound admitted calls across the whole user response scope. |

`agentcli.RequestEndTurn(ctx)` lets a handler request termination only for
specific successful results. Any failed, denied, declined, or interrupted call
in the same batch keeps the turn open.

An early `EndResponseScope` call is skipped without executing the handler. At
the final boundary the runtime exposes missing trigger tools in a bounded
repair round. Scope events are available with `SubscribeScopeEvents`.

## Safety hooks

Tools may add:

- `Permission` or `PermissionWithPolicy` for capability admission;
- `Confirmation` for an invocation-specific Yes/No decision;
- `ToolCallGuard` or `ToolCallGuardPrompt` for pre-handler argument policy.

See [Safety, permissions, and confirmations](permissions-and-confirmations.md)
for guardrails and execution policy.

Treat all model arguments as untrusted. Bound I/O and execution time, normalize
user-visible decision text, return structured JSON, and do not treat schemas,
prompts, permissions, or confirmations as a sandbox.
