---
title: Tool-output guards
sidebar_position: 3
---

# Tool-output guards

A custom tool can inspect its successful handler output with either a callback
or a prompt. Configure exactly one mode per tool.

## Function guard

```go
func checkLookupOutput(
    ctx context.Context,
    attempt agentcli.ToolOutputGuardAttempt,
) (agentcli.ToolOutputGuardDecision, error) {
    var output struct {
        Items []string `json:"items"`
    }
    if err := json.Unmarshal(attempt.Output, &output); err != nil {
        return agentcli.ToolOutputGuardDecision{
            Action:   agentcli.ToolOutputReject,
            Feedback: "Call lookup again; the result must contain valid items JSON.",
        }, nil
    }
    if len(output.Items) == 0 {
        return agentcli.ToolOutputGuardDecision{
            Action:   agentcli.ToolOutputReject,
            Feedback: "Call lookup again with a broader query.",
        }, nil
    }
    return agentcli.ToolOutputGuardDecision{
        Action: agentcli.ToolOutputProceed,
    }, nil
}
```

```go
lookup := agentcli.Tool{
    Definition: agentcli.ToolDefinition{
        Name:        "lookup",
        Description: "Look up application-owned records.",
        InputSchema: agentcli.ObjectSchema(/* parameters */),
    },
    Handler:         lookupHandler,
    TurnBehavior:    agentcli.ContinueTurn,
    ToolOutputGuard: checkLookupOutput,
}
```

The attempt carries `SessionID`, `TurnID`, `CallID`, `ToolName`, `Arguments`,
and `Output`. JSON buffers are independent copies, so mutating them cannot
change the request or published result.

## Prompt guard

```go
lookup := agentcli.Tool{
    Definition: agentcli.ToolDefinition{
        Name:        "lookup",
        Description: "Look up application-owned records.",
        InputSchema: agentcli.ObjectSchema(/* parameters */),
    },
    Handler: lookupHandler,
    ToolOutputGuardPrompt: `
Allow only valid JSON with a non-empty items array.
Reject stale or irrelevant items and tell the agent how to adjust the query.
`,
    ToolOutputGuardModel: &agentcli.GuardModelConfig{
        Provider: "policy",
        Model:    "guard-model-small",
    },
}
```

`ToolOutputGuardModel` is optional and valid only with
`ToolOutputGuardPrompt`. Its `GuardModelConfig` groups the provider/model pair
so the tool cannot configure one field while accidentally omitting the other.
Agent construction resolves the provider name from the loaded Project and
validates the local profile before starting the runtime. An unknown profile,
unsupported provider type, empty struct field, or model config on a function
guard fails construction.

When the config is omitted, the prompt guard uses the Agent's main model. This
keeps a simple tool declaration short:

```go
agentcli.Tool{
    // Definition and Handler omitted.
    ToolOutputGuardPrompt: "Allow only complete, policy-compliant results.",
}
```

Direct users of `toolexecution.NewExecutor` set
`Config.ToolOutputGuardModel` for fallback checks and
`Config.ToolOutputGuardModelResolver` when registered tools can select an
explicit provider/model pair.

## Rejection becomes agent feedback

The executor evaluates the guard after a handler returns valid JSON and before
marking the result successful:

```text
handler succeeds
  -> guard rejects
  -> raw output is discarded
  -> ToolResultFailed(error = guard feedback)
  -> ContinueTurn
  -> provider sees the failed tool result
  -> agent may call the tool again
```

This is not an automatic executor retry. A new call requires a new model tool
request and call ID. Failed guard infrastructure, a panic, malformed prompt
verdict, invalid decision, or invalid handler JSON also becomes a failed tool
result rather than terminating the executor.

Guard rejection overrides `EndTurn` with `ContinueTurn`. A later successful
retry retains the configured behavior. This also works for
`RequiredAtTurnEnd` tools: a rejected attempt does not satisfy the finalizer,
while a successful terminal retry can.

## Side effects and idempotency

The handler has already run when its output guard starts. Do not use an output
guard as the only check before sending email, charging money, editing files, or
publishing remote state. Validate dangerous preconditions in the handler,
request permission or confirmation before execution, and use an idempotency
key when the agent may retry.

The generated `report_discord` tool demonstrates a prompt output guard using
the main model fallback. It is a local network-free mock, validates arguments
before appending, and asks the guard to verify the result/argument
relationship: `skipReport: true` must return `skipped`, while an omitted or
false value must return `reported`. A rejected check returns feedback to the
main agent so it can issue a corrected finalizer call.

The report decision is owned by the agent, not the guard. The agent sets
`skipReport: true` only after deciding that the turn has no useful user-facing
content worth reporting. The handler then returns `skipped` without appending
to the report file. Otherwise the agent omits the option or sets it to `false`,
and the handler appends the message before returning `reported`. The prompt
guard verifies that argument/result relationship after the handler runs.
