---
title: Custom tools
sidebar_position: 1
---

# Custom tools

`WithTool` is the application-tool registration API. A tool explicitly owns
its model-facing definition, raw JSON handler, turn behavior, trigger mode,
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

## Tool trigger

The zero trigger is the default: the handler executes immediately and the
provider continues. Do not set a field for this mode.

`EndTurn` makes the tool a required trigger tool and runs the handler
immediately:

```go
Trigger: agentcli.EndTurn,
```

`Trigger` does not end the turn. After a successful call, the provider normally
receives another round. The completion guard remembers the latest result for
each required trigger tool during the turn; a later failed attempt makes that
tool unsatisfied again.

## Optional end after success

Use `EndTurnOnSuccess` when a successful tool batch should end the current
turn:

```go
agentcli.Tool{
    Definition:       definition,
    Handler:          handler,
    EndTurnOnSuccess: true,
}
```

Without `Trigger`, this tool remains optional and runs immediately. The runtime
waits for the whole parallel batch. If every result succeeds, one
`EndTurnOnSuccess` tool ends the turn even when the batch also contains normal
immediate tools. Any failed, denied, declined, or interrupted result continues
to another provider round. Required trigger tools are still enforced by the
completion guard, so this setting cannot bypass a missing `EndTurn` or
`EndResponseScope` trigger.

`EndTurnOnSuccess` controls only turn termination. It may be combined with
either trigger:

```go
agentcli.Tool{
    Definition:                         definition,
    Handler:                            handler,
    Trigger:                            agentcli.EndResponseScope,
    EndTurnOnSuccess:                   true,
    CanonicalAssistantMessageParameter: "message",
}
```

Here the invocation is required only at the final response-scope boundary.
Earlier model calls are skipped and continue the turn. When runtime completion
repair requests the tool, the handler executes and `EndTurnOnSuccess` may end
the turn. The optional canonical parameter must name a required string
property in the tool schema. After the handler succeeds, its exact argument
value is appended as the durable assistant message. Handler failure or
cancellation appends nothing.

## End-of-scope trigger tools

For a side effect that must happen once after the whole user response,
including subagent callbacks and follow-ups, set one trigger:

```go
agentcli.Tool{
    Definition: definition,
    Handler:    handler,
    Trigger:    agentcli.EndResponseScope,
}
```

If the model calls the tool during an ordinary provider round, the handler is
not called and no candidate is retained. The model receives:

```json
{
  "status": "skipped",
  "executed": false,
  "reason": "response_scope_not_ready_to_end",
  "instruction": "This tool only runs when the response scope is ready to end. Continue the remaining work and do not retry this tool now. The runtime will request it again at the correct time."
}
```

The outer tool result is successful so the model does not treat the skip as an
error, but `trigger_satisfied=false`. `EndTurnOnSuccess` is still honored, so a
final-delivery tool can yield the current turn while callbacks are pending
without executing its handler. The runtime does not invoke the handler,
permission resolver, confirmation resolver, or tool-call guard for this
skipped call. It also does not retain the arguments as a candidate for later
execution.

### How the runtime distinguishes an early call

The runtime does not infer intent from the tool's position or message text.
It requires both independent conditions below before executing an
`EndResponseScope` handler:

| Condition | Early provider round | Final completion repair |
| --- | --- | --- |
| The call was requested from a completion-repair boundary | No | Yes |
| This is the scope's last active turn and no callback is pending | Maybe | Yes |
| Handler executes and trigger is satisfied | No | Yes |

Therefore, even if `report_discord` is the first tool the model calls, its
request is not marked as a completion-boundary request. It receives the
successful skipped result above and the model continues. A normal assistant
completion attempt is what lets the completion guard determine whether the
scope is ready; only its restricted repair round can produce the executable
final call.

Intermediate turns with accepted callback obligations may finish without this
trigger, and their assistant drafts are discarded rather than becoming
conversation history.

When the last active turn attempts completion with no pending callbacks, the
completion guard exposes the missing `EndResponseScope` tools and adds:

```text
The response scope is ready to end. Call these required
end-response-scope tools now with the final completed response.
```

That repair call passes through admission and tool-call guards, runs cleanup,
executes the handler, and satisfies the trigger. `EndTurnOnSuccess` applies
only to this executed result. Handler failure remains unsatisfied and enters
bounded repair. The runtime allows up to three consecutive no-progress
repairs.

Required trigger tools should therefore be described as standalone final actions.

Several trigger tools are supported and may succeed in different tool batches
within the same turn. `EndTurnOnSuccess` is optional for each tool and affects
only whether its successful batch terminates the turn.

Applications can observe scope shutdown without adding another tool:

```go
events := agent.SubscribeScopeEvents(ctx)
for event := range events {
    switch event.Type {
    case agentcli.PreEndScope:
        // The final boundary was reached; cleanup and handlers have not run.
    case agentcli.EndScope:
        // Cleanup and final handler invocation are complete; the scope ended.
    }
}
```

The stream is live-only. Subscribe before starting the root turn when neither
boundary may be missed. `PreEndScope` precedes automatic subagent cleanup and
the first final `EndResponseScope` handler. `EndScope` is emitted only after
the turn completes, canonical messages are persisted, and the scope is removed.

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

## Guard requested tool calls

Each custom tool can configure exactly one pre-execution call-guard mode:

```go
ToolCallGuard: func(
    ctx context.Context,
    attempt agentcli.ToolCallGuardAttempt,
) (agentcli.ToolCallGuardDecision, error) {
    if !argumentsAreAllowed(attempt.Arguments) {
        return agentcli.ToolCallGuardDecision{
            Action:   agentcli.ToolCallReject,
            Feedback: "Call the tool again with a narrower query.",
        }, nil
    }
    return agentcli.ToolCallGuardDecision{
        Action: agentcli.ToolCallAllow,
    }, nil
},
```

Or attach a semantic policy:

```go
ToolCallGuardPrompt: `
Allow only specific, policy-compliant lookup requests.
Reject unsafe or overly broad arguments with concise retry instructions.
`,
ToolCallGuardModel: &agentcli.GuardModelConfig{
    Provider: "policy",
    Model:    "guard-model-small",
},
```

A rejection prevents the handler from executing and publishes a failed tool
result whose error contains the feedback. The agent receives that result on the
next provider round and may call the tool again with corrected arguments. See
[Tool-call guards](../guardrails/tool-call.md) for the complete lifecycle,
failure posture, prompt mode, and trigger tool behavior.

## Project allowlists

- `WithTool` registers the handler globally.
- `MAIN.md` selects which registered application tools the root sees.
- A subagent definition selects which registered application tools that child
  sees.

Required trigger tool behavior applies only to agents that expose that tool.
Subagents never receive root-only management tools and cannot nest.

## Handler checklist

- Honor cancellation through `ctx`.
- Treat model arguments as untrusted.
- Use `DecodeArguments`, then validate semantics and target state.
- Bound I/O, execution time, and output.
- Normalize argument-derived permission/confirmation text.
- Return structured JSON where practical.
- Do not treat schemas, prompts, permissions, or confirmations as a sandbox.
