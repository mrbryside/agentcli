---
title: Permissions and confirmations
sidebar_position: 3
---

# Permissions and confirmations

Permissions answer **may this capability execute?** Confirmations answer **does
the user want this specific invocation after seeing its details?** They are
independent gates.

## Permission declarations

A tool declares capability actions, `low`/`medium`/`high` risk, a reason, and
optional invocation details.

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

Actions are `FilesystemRead`, `FilesystemWrite`, `ProcessExecute`,
`NetworkAccess`, and `SandboxBypass`. Omitting both permission fields creates
an unguarded tool; do this only when appropriate for the host trust model.

## Dynamic permission and confirmation

Share strict decoding and normalization across the handler and descriptors:

```go
type publishArguments struct {
    Destination *string `json:"destination"`
    Message     *string `json:"message"`
}

func decodePublishArguments(raw json.RawMessage) (publishArguments, error) {
    var input publishArguments
    if err := agentcli.DecodeArguments(raw, &input); err != nil {
        return publishArguments{}, err
    }
    if input.Destination == nil || strings.TrimSpace(*input.Destination) == "" {
        return publishArguments{}, errors.New("destination is required")
    }
    if input.Message == nil || strings.TrimSpace(*input.Message) == "" {
        return publishArguments{}, errors.New("message is required")
    }
    return input, nil
}
```

```go
tool := agentcli.Tool{
    Definition: agentcli.ToolDefinition{
        Name:        "publish_report",
        Description: "Publish one report after user approval.",
        InputSchema: agentcli.ObjectSchema(struct {
            Destination agentcli.ToolParameter
            Message     agentcli.ToolParameter
        }{
            Destination: agentcli.StringParameter("Configured destination").
                Required().
                MinLength(1).
                MaxLength(120),
            Message: agentcli.StringParameter("Report body").
                Required().
                MinLength(1).
                MaxLength(4000),
        }),
    },
    Handler: publishReport,
    Permission: func(raw json.RawMessage) (
        agentcli.ToolPermissionDescription,
        error,
    ) {
        input, err := decodePublishArguments(raw)
        if err != nil {
            return agentcli.ToolPermissionDescription{}, err
        }
        return agentcli.ToolPermissionDescription{
            Actions: []agentcli.PermissionAction{agentcli.NetworkAccess},
            Risk:    agentcli.RiskHigh,
            Reason:  "Publishes a report to an external destination.",
            Details: "Destination: " + strings.TrimSpace(*input.Destination),
        }, nil
    },
    Confirmation: func(raw json.RawMessage) (
        agentcli.ToolConfirmationDescription,
        error,
    ) {
        input, err := decodePublishArguments(raw)
        if err != nil {
            return agentcli.ToolConfirmationDescription{}, err
        }
        return agentcli.ToolConfirmationDescription{
            Title:   "Confirm report publication",
            Message: "Publish this report now?",
            Details: "Destination: " + strings.TrimSpace(*input.Destination),
        }, nil
    },
}
```

The handler must validate again because a descriptor is not the execution
boundary. Normalize control characters, bound display text, and never include
secrets or unnecessary full content in permission/confirmation details.

`PermissionWithPolicy` is the alternative when classification needs the
immutable policy snapshot:

```go
PermissionWithPolicy: func(
    raw json.RawMessage,
    policy permission.Policy,
) (permission.Description, error) {
    input, err := decodePublishArguments(raw)
    if err != nil {
        return permission.Description{}, err
    }
    return classifyPublish(input, policy)
},
```

A tool may set `Permission` or `PermissionWithPolicy`, not both.

## Permission modes

| Mode | Default behavior |
| --- | --- |
| `default` | Ask for guarded calls. |
| `acceptEdits` | Allow exclusively filesystem-write calls; ask for others. |
| `criticalOnly` | Ask for high-risk calls; allow low/medium risk. |
| `dontAsk` | Deny calls that would need a question. |
| `plan` | Deny executable capabilities while planning. |
| `unrestricted` | Allow declared permissions without a question. This is host access, not a sandbox. |

Explicit rules use `deny > ask > allow > default` precedence. Change the mode
at runtime with `agent.SetPermissionMode`. Existing pending prompts stay
pending; new requests use the new policy epoch.

## Non-interactive execution

`WithNonInteractive(true)` is an execution flag, not a permission mode.
Admission still evaluates mode, rules, and grants:

```text
permission allow  → allow
permission deny   → deny
permission ask    → deny
confirmation      → No / declined
```

It does not change `Agent.PermissionMode()` or emit a mode-change event.
`criticalOnly` therefore allows low/medium risk but denies a high-risk request
that would ask. `unrestricted` allows declared permissions unless an explicit
rule asks or denies, but confirmation is still declined.

## Permission before confirmation

For a tool with both gates, permission admission runs first. Only an allowed
permission produces the invocation-specific confirmation. A session/project
grant may suppress a later permission question, but every invocation can still
require confirmation. Yes runs the handler; No produces a `declined` tool
result. Interruption cancels pending admission.

## Resolve decisions

Preserve every correlation ID:

```go
err := agent.ResolvePermission(ctx, permission.Decision{
    PermissionID: request.ID,
    SessionID:    request.SessionID,
    TurnID:       request.TurnID,
    CallID:       request.CallID,
    Type:         permission.AllowOnce,
})
```

Decision types are `AllowOnce`, `AllowSession`, `AllowProject`, and `Deny`.

```go
err := agent.ResolveConfirmation(ctx, confirmation.Decision{
    ConfirmationID: request.ID,
    SessionID:      request.SessionID,
    TurnID:         request.TurnID,
    CallID:         request.CallID,
    Answer:         confirmation.Yes,
})
```

## Interactive decision ordering

The reference Terminal presents one global FIFO across root sessions,
subagents, permissions, and confirmations. It shows one actionable question at
a time; after it resolves, the next oldest request becomes visible. A numeric
permission shortcut cannot consume a visible confirmation, and `y`/`n` cannot
consume a visible permission. Explicit ID commands remain available.

The executor itself publishes at most one admission prompt per session. Other
sessions can continue independently, and waiting requests do not occupy worker
slots.

## Late answers

Requests remain correlated by decision ID, session, turn, and call. A client
may answer later while the process and pending run still exist. Duplicate,
mismatched, expired, cancelled, and post-interruption answers fail safely.

Generated starter edits demonstrate both gates with a high-risk
`filesystem.write` permission followed by confirmation. See
[Bootstrap a project](../getting-started/bootstrap-project.md).
