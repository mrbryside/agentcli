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

## Non-interactive execution

`agentcli.WithNonInteractive(true)` is **not** a permission mode. It is an
independent execution flag for one-shot jobs, background workers, tests, and
other hosts that have no UI available to answer a question.

Admission still evaluates the configured permission mode, explicit policy
rules, and existing grants first. Non-interactive handling changes only the
outcomes that require human input:

```text
permission allow  → allow
permission deny   → deny
permission ask    → deny
confirmation      → No / declined
```

It does not change `Agent.PermissionMode()`, emit a permission-mode event, or
disable tools that the current policy already allows.

| Permission mode | Effective non-interactive behavior |
| --- | --- |
| `default` | Guarded calls that would ask are denied. |
| `acceptEdits` | Filesystem-write-only calls are allowed; other calls that would ask are denied. |
| `criticalOnly` | Low/medium-risk calls are allowed; high-risk calls that would ask are denied. |
| `dontAsk` | Guarded calls are denied by the mode as usual. |
| `plan` | Executable capabilities are denied by the mode as usual. |
| `unrestricted` | Declared permissions are allowed unless an explicit policy rule asks or denies; confirmation is still declined. |

For example, a one-shot terminal command has no input loop, so it should not
wait forever for a permission answer:

```go
nonInteractive := initialPrompt != ""

agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithNonInteractive(nonInteractive),
)
```

Use `false` for an interactive terminal or UI that renders and resolves
permission and confirmation requests. Use `true` only when the host cannot
answer them.

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

Generated starter edits demonstrate both gates together: `edit` declares a
dynamic high-risk `filesystem.write` permission and an invocation-specific
confirmation. In the default `criticalOnly` project, permission is published
first; only an allowed request produces the confirmation. A session/project
permission grant can suppress a later permission question, but every edit call
still asks for confirmation. Denial or confirmation No leaves the file
unchanged.

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
