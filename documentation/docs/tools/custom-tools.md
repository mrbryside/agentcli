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

`EndTurn` makes the tool a required trigger tool, runs the handler immediately,
and permits completion without another provider request:

```go
Trigger: agentcli.EndTurn,
```

The runtime waits for every call in a parallel batch. It ends the turn only
when all results succeeded and all called tools resolve to `EndTurn`. A mixed,
failed, denied, declined, or interrupted batch continues.

## Optional end after success

Use `EndTurnOnSuccess` when a tool should stay optional, run immediately, and
end the current turn when it succeeds:

```go
agentcli.Tool{
    Definition:       definition,
    Handler:          handler,
    EndTurnOnSuccess: true,
}
```

The runtime waits for the whole parallel batch. If every result succeeds, one
`EndTurnOnSuccess` tool ends the turn even when the batch also contains normal
immediate tools. Any failed, denied, declined, or interrupted result continues
to another provider round. Required trigger tools are still enforced by the
completion guard, so this setting cannot bypass a missing `EndTurn` or
`EndResponseScope` trigger.

`EndTurnOnSuccess` does not make the tool required and does not defer its
handler. Do not combine it with `Trigger`; registration rejects that ambiguous
configuration.

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

The model receives a successful `status=deferred` tool result while the scope
is active; the handler is not called yet. The runtime retains the latest
candidate and invokes it exactly once when every turn and accepted subagent
callback in the response scope has settled. Before invoking deferred handlers,
the runtime automatically reconciles children touched by that scope: unshared
completed/failed children close, while incomplete children remain available
for follow-up. Only a successful terminal batch satisfies completion. That
batch must either contain only `EndTurn` tools or include an
`EndTurnOnSuccess` tool; an earlier or continuing batch does not. If the model
attempts to finish while a
trigger tool is missing, the completion guard starts another provider round with a
reminder naming every missing trigger tool and exposes only those tools. A
caller-supplied completion guard may merge additional bounded allowlist entries.
AgentRuntime does not set provider-specific tool choice. It permits up to three
consecutive no-progress repairs; progress resets that budget.

Required trigger tools should therefore be described as standalone final actions.

Several trigger tools are supported. The terminal batch must successfully
include every still-missing trigger tool. A normal continuing tool may share
that batch only when another tool has `EndTurnOnSuccess: true`.

Applications can observe scope shutdown without adding another tool:

```go
events := agent.SubscribeScopeEvents(ctx)
for event := range events {
    switch event.Type {
    case agentcli.PreEndScope:
        // The scope is quiescent; cleanup and staged handlers have not run.
    case agentcli.EndScope:
        // Cleanup and staged handler invocation are complete; the scope ended.
    }
}
```

The stream is live-only. Subscribe before starting the root turn when neither
boundary may be missed. `PreEndScope` always precedes automatic subagent
cleanup and staged `EndResponseScope` handlers. `EndScope` is emitted only
after those operations and scope removal.

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
