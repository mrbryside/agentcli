---
title: Safety & Permissions
sidebar_position: 2
---

# Safety, Permissions, and Confirmations

Permissions answer “may this capability execute?” Confirmations answer “does
the user want this specific invocation?” They are separate gates.

## Declare capability risk

```go
tool := agentcli.Tool{
    Definition: definition,
    Handler:    handler,
    Permission: agentcli.ToolStaticPermission(
        agentcli.ToolPermissionConfig{
            Actions: []agentcli.PermissionAction{
                agentcli.FilesystemRead,
            },
            Risk:   agentcli.RiskLow,
            Reason: "Reads one bounded project text file.",
        },
    ),
}
```

Actions include filesystem read/write, process execution, network access, and
sandbox bypass. Omitting permission creates an unguarded tool; do that only
when it fits the application's trust model.

For argument-dependent risk, use a `Permission` function. Add `Confirmation`
when the user should inspect the exact target immediately before execution:

```go
Permission: func(raw json.RawMessage) (agentcli.ToolPermissionDescription, error) {
    input, err := decodePublishArguments(raw)
    if err != nil {
        return agentcli.ToolPermissionDescription{}, err
    }
    return agentcli.ToolPermissionDescription{
        Actions: []agentcli.PermissionAction{agentcli.NetworkAccess},
        Risk:    agentcli.RiskHigh,
        Reason:  "Publishes a report externally.",
        Details: "Destination: " + input.Destination,
    }, nil
},
Confirmation: func(raw json.RawMessage) (agentcli.ToolConfirmationDescription, error) {
    input, err := decodePublishArguments(raw)
    if err != nil {
        return agentcli.ToolConfirmationDescription{}, err
    }
    return agentcli.ToolConfirmationDescription{
        Title:   "Publish report",
        Message: "Publish this report now?",
        Details: "Destination: " + input.Destination,
    }, nil
},
```

Decode and normalize arguments again in the handler. Never put credentials or
unbounded model content in decision details.

## Permission modes

| Mode | Default behavior |
| --- | --- |
| `default` | Ask for guarded calls. |
| `acceptEdits` | Allow filesystem-write-only calls; ask for others. |
| `criticalOnly` | Allow low/medium risk; ask for high risk. |
| `dontAsk` | Deny calls that would ask. |
| `plan` | Deny executable capabilities while planning. |
| `unrestricted` | Allow declared permissions unless a rule asks or denies. |

Explicit rules use `deny > ask > allow > default` precedence. Mode changes
affect new requests; existing pending requests remain correlated and pending.

### Non-interactive execution

`WithNonInteractive(true)` is not a mode. It preserves policy evaluation, then
denies anything that would ask and declines confirmations. Even
`unrestricted` does not bypass confirmations.

## Decision lifecycle

Permission runs before confirmation. An allowed permission may be remembered
once, for the session, or for the project; every invocation can still require
its own confirmation.

Resolve requests with their full permission/confirmation ID, session ID, turn
ID, and call ID. Duplicate, mismatched, expired, cancelled, and
post-interruption answers fail safely.

The Terminal presents one FIFO across main and task sessions. The executor
publishes at most one admission prompt per session, and waiting decisions do
not consume tool-worker slots.

## Security boundary

Permissions and confirmations are application policy, not containment. Tool
handlers must still:

- resolve and constrain filesystem paths;
- parse shell commands instead of relying on substring checks;
- enforce authorization and destination allowlists;
- limit time, bytes, result counts, and output size;
- support cancellation and idempotency where side effects are possible;
- keep HTTP APIs on loopback until authentication, TLS, CORS, and rate limits
  are configured.

The optional `toolexecution/bashsecure` package helps classify and scope shell
commands, but the host remains responsible for the final operating-system
sandbox and deployment policy.

## Guardrails

Guardrails inspect input, pending output, or a requested tool call. Use
function guards for deterministic rules and prompt guards for narrow semantic
policies. They complement permissions and handler validation.

### Input and output

```go
func checkInput(ctx context.Context, attempt agentcli.InputGuardAttempt) (
    agentcli.InputGuardDecision,
    error,
) {
    if strings.Contains(attempt.Message.Content, "blocked-value") {
        return agentcli.InputGuardDecision{
            Action:   agentcli.InputRespond,
            Response: "I can help with a safer version of that request.",
        }, nil
    }
    return agentcli.InputGuardDecision{Action: agentcli.InputAccept}, nil
}

func checkOutput(ctx context.Context, attempt agentcli.OutputGuardAttempt) (
    agentcli.OutputGuardDecision,
    error,
) {
    if strings.Contains(attempt.Output.Content, "private-value") {
        return agentcli.OutputGuardDecision{
            Action:   agentcli.OutputRetry,
            Feedback: "Rewrite without private values.",
        }, nil
    }
    return agentcli.OutputGuardDecision{Action: agentcli.OutputProceed}, nil
}
```

Register them with `WithInputGuard` and `WithOutputGuard`. Input can be
accepted, replaced, answered without the main model, or rejected. Output can
proceed or request a repair round. Rejected output stays in run events for
diagnostics but is not stored in the conversation.

Prompt-backed equivalents use `WithInputGuardPrompt` and
`WithOutputGuardPrompt`; an optional configured provider/model can isolate the
policy call. Do not configure function and prompt modes for the same direction.

### Tool calls

```go
ToolCallGuard: func(ctx context.Context, attempt agentcli.ToolCallGuardAttempt) (
    agentcli.ToolCallGuardDecision,
    error,
) {
    if !argumentsAreAllowed(attempt.Arguments) {
        return agentcli.ToolCallGuardDecision{
            Action:   agentcli.ToolCallReject,
            Feedback: "Retry with one specific, allowed query.",
        }, nil
    }
    return agentcli.ToolCallGuardDecision{Action: agentcli.ToolCallAllow}, nil
},
```

`ToolCallGuardPrompt` provides a semantic alternative and may select a model
with `ToolCallGuardModel`. Rejection happens before the handler, becomes a
failed tool result, and lets the main model issue a corrected call. It is not
an automatic executor retry.

Prompt guards use one isolated, tools-disabled model request and require a
strict `allowed`, `reason`, and `feedback` JSON verdict. Malformed or
unavailable guard results fail closed. Keep policies narrow, avoid secrets in
feedback, and test rejection, malformed output, timeout, and cancellation.
