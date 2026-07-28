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

## Require skills before execution

Set `RequiredSkills` when a custom tool must not execute until the model has
loaded specific workflow instructions:

```go
agentcli.Tool{
    Definition: searchDefinition,
    Handler:    searchHandler,
    RequiredSkills: []string{
        "web-research",
    },
}
```

Every listed skill must be available to the agent using the tool. For each new
turn, the model must call `load_skill` and receive `status=loaded` for that
exact skill before calling the custom tool. Both a full `instructions` result
and an `instructions_in_context=true` result satisfy the requirement. A load
from an earlier turn does not. The load result's `tools_unchanged: true` field
confirms that loading instructions did not alter the tools listed with the
current request; `RequiredSkills` controls execution admission, not tool
registration.

Registration automatically adds this requirement to the cloned tool
description. The runtime also enforces it. If any required skill is missing,
admission, call-budget reservation, guards, and the handler are skipped:

```json
{
  "status": "blocked",
  "executed": false,
  "reason": "required_skill_not_loaded",
  "required_skills": ["web-research"],
  "missing_skills": ["web-research"],
  "instruction": "Call load_skill once for each missing skill. A status=loaded result for that skill in this turn satisfies the requirement, including when instructions_in_context=true. Then retry this tool."
}
```

Loading a skill and calling its dependent tool in the same parallel tool batch
does not bypass the gate: the load result must first be present in the current
turn's conversation context.

## Tool trigger

The zero trigger is the default: the handler executes immediately and the
provider continues. Do not set a field for this mode.

When `Trigger` or `EndTurnOnSuccess` is configured, registration automatically
appends a runtime-owned paragraph to the cloned tool description. The model
therefore sees when it should call the tool, whether its handler runs
immediately or only at the final response-scope boundary, what an early call
returns, and whether success ends the current turn. Keep the application
description focused on the tool's domain purpose and arguments; agentcli adds
the execution-mode guidance without mutating the caller's original
`ToolDefinition`.

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
    Definition:       definition,
    Handler:          handler,
    Trigger:          agentcli.EndResponseScope,
    EndTurnOnSuccess: true,
}
```

Here the invocation is required only at the final response-scope boundary.
Earlier model calls are skipped and continue the turn. When runtime completion
repair requests the tool, the handler executes and `EndTurnOnSuccess` may end
the turn. The durable transcript retains the tool call and its result without
adding a synthetic assistant message from the arguments.

### Conditional end from a handler

When only some successful invocations should end the turn, keep
`EndTurnOnSuccess` false and call `RequestEndTurn` from the handler condition:

```go
agentcli.Tool{
    Definition: definition,
    Handler: func(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
        var input struct {
            Finish bool `json:"finish"`
        }
        if err := agentcli.DecodeArguments(arguments, &input); err != nil {
            return nil, err
        }
        output := json.RawMessage(`{"status":"done"}`)
        if input.Finish {
            if err := agentcli.RequestEndTurn(ctx); err != nil {
                return nil, err
            }
        }
        return output, nil
    },
}
```

The request takes effect only when that handler and every other call in the
same tool batch succeed. A failed, denied, declined, or interrupted call
continues to another provider round. Required trigger tools are still enforced.
Because the condition is application-specific, describe the controlling input
and its true/false behavior in the tool description or parameter schema.
Calling `RequestEndTurn` outside the active handler context returns an error.

## End-of-scope trigger tools

For a side effect that must happen once after the whole user response,
including subagent results and follow-ups, set one trigger:

```go
agentcli.Tool{
    Definition: definition,
    Handler:    handler,
    Trigger:    agentcli.EndResponseScope,
}
```

For an ordinary tool that must not exceed a cumulative response-scope budget,
set `ResponseScopeCallLimit`. The main-agent turn, inline results, and result
continuations share the counter:

```go
agentcli.Tool{
    Definition:            searchDefinition,
    Handler:               searchHandler,
    ResponseScopeCallLimit: 2,
}
```

An over-budget call returns successful `status=skipped`,
`reason=response_scope_tool_budget_exhausted`, and never reaches admission,
the handler, or the network. Admitted attempts count even if they later fail.

If the model calls the tool as the human main-agent turn's first provider action, or
while the response scope is busy, the handler is not called and no candidate
is retained. The model receives:

```json
{
  "status": "succeeded",
  "action": "skipped",
  "executed": false,
  "reason": "tool_called_at_wrong_time",
  "instruction": "The tool call was processed successfully, but the tool action was skipped because this end-of-scope tool was called at the wrong time. Treat this result as success and do not retry the tool yourself."
}
```

The result is successful so the model does not treat the skip as an error, but
`trigger_satisfied=false`. `EndTurnOnSuccess` is still honored, so a
final-delivery tool can yield the current turn while results are pending
without executing its handler. When no result or other active turn keeps the
scope open, the same premature call continues the current turn so normal tools
remain available. The runtime does not invoke the handler, permission resolver,
confirmation resolver, or tool-call guard for this skipped call. It also does
not retain the arguments as a candidate for later execution.

### How the runtime distinguishes an early call

The runtime does not infer intent from message text. It requires both
independent conditions below before executing an
`EndResponseScope` handler:

| Condition | Human main-agent step 1 | Later main-agent round | Result turn step 1 | Completion repair |
| --- | --- | --- | --- | --- |
| Initial-action guard has passed | No | Yes | Yes | Yes |
| Last active turn; no result/input pending | Maybe | Required | Required | Required |
| Handler executes and trigger is satisfied | No | Yes | Yes | Yes |

Therefore, even if `report_discord` is the first tool the model calls, its
request receives the successful skipped result above and the model continues.
The model-facing instruction says not to retry the tool itself. It finishes the
remaining work and attempts normal completion, at which point runtime repair
requests the final call. A result continuation is already past the human
first-action boundary, so it may deliver on provider step one.

The coordinator still accepts a quiescent call from a later main-agent provider round
for compatibility, but this is a permissive runtime fallback rather than the
retry path described to the model.

Intermediate turns with accepted result obligations may finish without this
trigger, and their assistant drafts are discarded rather than becoming
conversation history.

When the last active turn attempts completion with no pending results, the
completion guard exposes the missing `EndResponseScope` tools and adds:

```text
The response scope is ready to end. Call these required
end-response-scope tools now with the final completed response.
```

That repair call passes through admission and tool-call guards, runs cleanup,
executes the handler, and satisfies the trigger. The compatibility path for a
quiescent later-round call reaches the same execution boundary.
`EndTurnOnSuccess` applies only to an executed result. Handler failure remains
unsatisfied and enters bounded repair. The runtime allows up to three
consecutive no-progress repairs.

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

The stream is live-only. Subscribe before starting the main-agent turn when neither
boundary may be missed. `PreEndScope` precedes final response-scope handlers and
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
- `MAIN.md` selects which registered application tools the main agent sees.
- A subagent definition selects which registered application tools that subagent
  sees.

Required trigger tool behavior applies only to agents that expose that tool.
Subagents never receive main-agent-only management tools and cannot nest.

## Handler checklist

- Honor cancellation through `ctx`.
- Treat model arguments as untrusted.
- Use `DecodeArguments`, then validate semantics and target state.
- Bound I/O, execution time, and output.
- Normalize argument-derived permission/confirmation text.
- Return structured JSON where practical.
- Do not treat schemas, prompts, permissions, or confirmations as a sandbox.
