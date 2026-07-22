---
title: Permissions and confirmations
sidebar_position: 3
---

# Permissions and confirmations

Permissions answer **may this capability execute?** Confirmations answer **does
the user want this specific invocation after seeing its details?** They are
separate gates.

## Permission declarations

A tool declares one or more actions:

- `filesystem.read`
- `filesystem.write`
- `process.execute`
- `network.access`
- `sandbox.bypass`

It also declares `low`, `medium`, or `high` risk, a user-facing reason, and
optional invocation details.

```go
agentcli.StaticToolPermission(toolexecution.PermissionConfig{
    Actions: []permission.Action{
        permission.FilesystemWrite,
        permission.NetworkAccess,
    },
    Risk:   permission.RiskHigh,
    Reason: "Uploads a generated report.",
})
```

Omitting every permission option deliberately creates an unguarded tool.
Choose that only for capabilities that are safe under your application's trust
model.

## Permission modes

| Mode | Default behavior |
| --- | --- |
| `default` | Ask for guarded calls. |
| `acceptEdits` | Automatically allow tools whose actions are exclusively filesystem writes; ask for others. |
| `criticalOnly` | Ask for high-risk calls; allow low/medium risk. |
| `dontAsk` | Deny calls that would need a question. |
| `plan` | Deny executable capabilities while planning. |
| `unrestricted` | Allow declared permissions without a question. This is full host access, not a sandbox. |

Explicit policy rules use `deny > ask > allow > default` precedence. An
explicit deny still wins in broader modes.

Change mode at runtime:

```go
err := agent.SetPermissionMode(ctx, permission.CriticalOnly)
```

Active runs receive `permission_mode_changed`. Already pending prompts remain
pending; new requests use the new policy epoch.

## Resolve a permission

Render the complete request to the user, then preserve all correlation IDs:

```go
decision := permission.Decision{
    PermissionID: request.ID,
    SessionID:    request.SessionID,
    TurnID:       request.TurnID,
    CallID:       request.CallID,
    Type:         permission.AllowOnce,
}
err := agent.ResolvePermission(ctx, decision)
```

Decision types are `allow_once`, `allow_session`, `allow_project`, and `deny`.
Session/project grants are held by permission storage. The initial in-memory
implementation survives late answers within the process, but not restarts.

## Add confirmation

```go
agentcli.ToolConfirmation(func(input deployInput) (confirmation.Description, error) {
    return confirmation.Description{
        Title:   "Deploy release",
        Message: "Deploy this version now?",
        Details: fmt.Sprintf("Version: %s\nEnvironment: %s", input.Version, input.Environment),
    }, nil
})
```

Resolve it with exact correlation:

```go
err := agent.ResolveConfirmation(ctx, confirmation.Decision{
    ConfirmationID: request.ID,
    SessionID:      request.SessionID,
    TurnID:         request.TurnID,
    CallID:         request.CallID,
    Answer:         confirmation.Yes, // or confirmation.No
})
```

Yes runs the handler. No produces a `declined` tool result and lets the model
continue. `unrestricted` never bypasses confirmation. Interruption cancels it,
and non-interactive mode declines it.

## Late answers

Requests remain correlated by decision ID, session, turn, and call. A terminal
or web client may answer much later while the process and pending run still
exist. Duplicate, mismatched, expired, cancelled, and post-interruption answers
fail safely.

Waiting permission/confirmation requests do not occupy worker-pool slots.

