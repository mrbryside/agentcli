---
title: Tool-call guards
sidebar_position: 3
---

# Tool-call guards

A custom tool can inspect the tool name and arguments requested by the model
with either a callback or a prompt. The guard runs before the handler, so a
rejected call cannot cause handler side effects. Configure exactly one mode per
tool.

:::note API change

The post-execution `ToolOutputGuard*` API has been removed. Use
`ToolCallGuard*` for pre-execution policy. Validate data produced by the handler
inside the handler before returning it.

:::

## Function guard

```go
func checkLookupCall(
    ctx context.Context,
    attempt agentcli.ToolCallGuardAttempt,
) (agentcli.ToolCallGuardDecision, error) {
    var input struct {
        Query string `json:"query"`
    }
    if err := agentcli.DecodeArguments(attempt.Arguments, &input); err != nil {
        return agentcli.ToolCallGuardDecision{
            Action:   agentcli.ToolCallReject,
            Feedback: "Call lookup again with one valid query.",
        }, nil
    }
    if strings.TrimSpace(input.Query) == "" {
        return agentcli.ToolCallGuardDecision{
            Action:   agentcli.ToolCallReject,
            Feedback: "Call lookup again with a non-empty query.",
        }, nil
    }
    return agentcli.ToolCallGuardDecision{
        Action: agentcli.ToolCallAllow,
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
    ToolCallGuard: checkLookupCall,
}
```

The attempt carries `SessionID`, `TurnID`, `CallID`, `ToolName`, and
`Arguments`. The arguments are a defensive copy, so mutating them cannot
change the handler input.

## Prompt guard

```go
lookup := agentcli.Tool{
    Definition: agentcli.ToolDefinition{
        Name:        "lookup",
        Description: "Look up application-owned records.",
        InputSchema: agentcli.ObjectSchema(/* parameters */),
    },
    Handler: lookupHandler,
    ToolCallGuardPrompt: `
Allow only a specific, policy-compliant lookup query.
Reject broad or unsafe requests and tell the agent how to adjust the arguments.
`,
    ToolCallGuardModel: &agentcli.GuardModelConfig{
        Provider: "policy",
        Model:    "guard-model-small",
    },
}
```

`ToolCallGuardModel` is optional and valid only with
`ToolCallGuardPrompt`. Its `GuardModelConfig` groups the provider/model pair
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
    ToolCallGuardPrompt: "Allow only specific, policy-compliant requests.",
}
```

Direct users of `toolexecution.NewExecutor` set
`Config.ToolCallGuardModel` for fallback checks and
`Config.ToolCallGuardModelResolver` when registered tools can select an
explicit provider/model pair. Each prompt guard has a 30-second timeout by
default; root Agents can override it with `WithToolCallGuardTimeout`, while
direct executor users set `Config.ToolCallGuardTimeout`.

## Rejection becomes agent feedback

The executor evaluates the guard after permission and confirmation admission
and immediately before invoking the handler:

```text
model requests tool call
  -> permission/confirmation admission
  -> guard rejects name/arguments
  -> handler is not called
  -> ToolResultFailed(error = guard feedback)
  -> ContinueTurn
  -> provider sees the failed tool result
  -> agent may call the tool again
```

This is not an automatic executor retry. A new call requires a new model tool
request and call ID. Failed guard infrastructure, a panic, malformed prompt
verdict, or invalid decision also becomes a failed tool result without invoking
the handler. Invalid JSON returned by an allowed handler remains a normal
failed tool result.

Guard rejection overrides `EndTurn` with `ContinueTurn`. A later successful
retry retains the configured behavior. This also works for
`RequiredAtTurnEnd` tools: a rejected attempt does not satisfy the finalizer,
while a successful terminal retry can.

## Side-effect boundary

Rejected calls do not execute the handler. An allowed call still relies on
permissions, confirmations, handler validation, and idempotency for its actual
side effects. A semantic guard does not replace authorization or containment.

The generated `report_discord` tool demonstrates a prompt tool-call guard using
the main model fallback. It asks the guard to check message bounds, disclosure
policy, direct standalone reporting, and the `skipReport` decision before the
handler can append anything. The message must state actions, current progress,
status, findings, or conclusions as if the reporting agent performed the work
itself. Ongoing progress is valid reportable content. It cannot mention or
imply delegation, attribute work to another agent/subagent/researcher, describe
waiting for one, or promise a later update. Internally delegated work must be
presented as the reporting agent's own action. For example, the guard rejects
`A subagent is analyzing main.go and will report back` but accepts `Analyzing
main.go to prepare a summary of its purpose, architecture, and key components.`
A rejected check returns feedback to the main agent, leaves the report file
unchanged, and allows a corrected finalizer call. When useful progress exists,
the feedback preserves it, removes delegation attribution, includes a concrete
suggested message, and does not recommend skipping the report.

The report decision is owned by the agent, not the guard. The agent sets
`skipReport: true` only after deciding that the turn has no meaningful
user-facing action, progress, status, finding, or conclusion. The guard
validates the submitted arguments, but it does not receive the full main-agent
conversation. Therefore, it can require a direct rewrite whenever the submitted
message itself contains useful progress, while the agent remains responsible
for deciding whether an otherwise content-free turn should be skipped. Once
allowed, the handler returns `skipped` without appending when the option is
true; otherwise it appends the message and returns `reported`.
