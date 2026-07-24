---
title: Go API
sidebar_position: 3
---

# Go API

This page summarizes the high-level Go surface. Detailed behavior is covered in
the feature guides.

## Project

```go
project, err := agentcli.LoadProject(root)
```

Useful immutable accessors:

```go
project.Root()
project.ProviderName()
project.ModelName()
project.PermissionMode()
project.MaxSubagents()
project.MainAgent()
project.Skills()
project.Subagents()
project.ToolNames()
project.SystemPrompts()
project.Model()
project.ModelFor(providerName, modelName)
```

## Agent construction

```go
agent, err := agentcli.New(ctx, options...)
```

Common options:

| Option | Purpose |
| --- | --- |
| `WithProject` | Apply provider/model/prompts/mode/skills/subagents from disk. |
| `WithModel` | Supply a model without project loading. |
| `WithCustomTool` | Register a typed tool with functional options. |
| `WithTool` | Register an advanced raw tool. |
| `WithPermissionMode` | Set initial mode. |
| `WithPermissionPolicy` | Supply explicit capability rules. |
| `WithNonInteractive` | Independent unattended-run flag: convert permission `ask` to `deny` and decline confirmations without changing permission mode. |
| `WithToolWorkers` | Set handler worker concurrency; default 4. |
| `WithChannelBuffer` | Set internal transport buffer; default 64. |
| `WithMessageStorage` | Replace transcript storage. |
| `WithPermissionStorage` | Replace permission/grant storage. |
| `WithConfirmationStorage` | Replace confirmation storage. |
| `WithSubagentStorage` | Replace child relationship storage. |
| `WithMaxSubagents` | Bound open children per parent session; overrides `config.yaml`. |
| `WithSystemPrompt` | Add ephemeral provider instructions. |
| `WithContextReminderProvider` | Add trusted per-round context not persisted in messages. |

## Tool handler context

Tool handlers receive invocation metadata through `context.Context`:

```go
invocation, ok := agentcli.ToolInvocationFromContext(ctx)
```

The returned `agentcli.ToolInvocation` includes `SessionID`, `TurnID`, `CallID`,
and `ToolName`. The runtime attaches it automatically before execution.
`WithToolInvocation` is provided for direct handler tests and adapters that
invoke a handler outside the executor.

The immutable admission policy is available with
`agentcli.ToolPermissionPolicyFromContext(ctx)`. Handlers may inspect it for
policy-aware behavior, but should not mutate it or treat it as a substitute for
permission checks.

## Typed tool options

```go
agentcli.StaticToolPermission(config)
agentcli.ToolPermission(typedDescriptor)
agentcli.ToolPermissionWithPolicy(typedDescriptor)
agentcli.ToolConfirmation(typedDescriptor)
agentcli.ToolSchema(rawObjectSchema)
agentcli.ToolTurnBehavior(agentcli.ContinueTurn)
agentcli.ToolTurnBehavior(agentcli.EndTurn)
agentcli.ToolRequiredAtTurnEnd()
```

`NewCustomTool` constructs a `toolexecution.Tool` without adding it to an agent.
`ContinueTurn` is the default. `EndTurn` stores a successful tool result and
allows the current turn to complete without another provider call when every
result in that batch also succeeds with `EndTurn`; mixed or non-success batches
continue so the model can dispatch more work or report the failure.

## Turns

```go
run, err := agent.Start(ctx, request)
run, subscription, err := agent.StartSubscribed(ctx, request)
```

Important `Run` methods:

```go
run.SessionID()
run.TurnID()
run.Status()
run.Done()
run.Result()
run.Events()
run.EventsBetween(after, through)
run.Subscribe(ctx)
run.Interrupt(ctx, reason)
```

Use `agent.ListMessages(ctx, sessionID)` for the transcript, not `Run.Events()`.

## Decisions and permission mode

```go
agent.ResolvePermission(ctx, permission.Decision{...})
agent.ResolveConfirmation(ctx, confirmation.Decision{...})
agent.PermissionMode()
agent.SetPermissionMode(ctx, permission.CriticalOnly)
```

`WithNonInteractive(true)` does not select a permission mode. The configured
mode still decides `allow`, `ask`, or `deny`; the flag denies only `ask`
outcomes because no UI is available, and it declines every required Yes/No
confirmation. See [Permissions and confirmations](../tools/permissions-and-confirmations.md#non-interactive-execution).

## Subagents

Application-facing methods include:

```go
agent.SubagentDefinitions()
agent.StartSubagent(ctx, parentSessionID, parentTurnID, name, message, label)
agent.SendSubagentMessage(ctx, parentSessionID, subagentID, message)
agent.ListSubagents(ctx, parentSessionID, includeClosed)
agent.CloseSubagent(ctx, parentSessionID, subagentID)
agent.InterruptSubagent(ctx, parentSessionID, subagentID, reason)
agent.SubscribeSubagentCallbacks(ctx)
agent.SubscribeSubagentPermissions(ctx)
agent.PendingSubagentPermissions(ctx, parentSessionID)
agent.SubscribeSubagentConfirmations(ctx)
agent.PendingSubagentConfirmations(ctx, parentSessionID)
agent.ContinueSubagentCallbackSubscribed(ctx, callback)
agent.ReadSubagent(ctx, parentSessionID, subagentID, afterMessageID)
agent.WaitSubagent(ctx, parentSessionID, subagentIDs, afterVersions)
```

Child decision methods require parent and child ownership in addition to the
normal correlated decision:

```go
agent.ResolveSubagentPermission(ctx, parentID, childID, decision)
agent.ResolveSubagentConfirmation(ctx, parentID, childID, decision)
```

Standard children evaluate the parent's permission policy and mode. Permission
and confirmation requests are sent to the parent event stream and remain
recoverable through `PendingSubagentPermissions` and
`PendingSubagentConfirmations`; the parent session UI, not a child UI or the
main model, supplies the decision.

## Reference terminal

```go
err := agent.RunTerminal(
    agentcli.WithTerminalSessionID("manual-check"),
)
```

`RunTerminal` is a blocking, reusable playground for the same runtime, storage,
tools, permissions, confirmations, skills, and subagents owned by the Agent.
Exiting it does not call `Agent.Close`, so the caller can continue with direct
turns or `RunServer`. Available functional options are
`WithTerminalSessionID`, `WithTerminalInput`, `WithTerminalOutput`, and
`WithTerminalInitialPrompt`.

## Server

```go
agent.RunServer(serverOptions...)

server, err := agentcli.NewServer(agent, serverOptions...)
server.Handler()
server.Echo()
server.Run()
server.Shutdown(ctx)
```

Options: `WithServerAddress`, `WithServerRequestLimit`,
`WithServerHeartbeat`, `WithServerTurnQueueLimit`, and
`WithServerMiddleware`. The queue option bounds accepted waiting root turns per
session; it does not change direct `Agent.Start` admission.

## Lifecycle

Always close an agent:

```go
defer agent.Close()
```

Close cancels active runs and the executor, closes subagent management, and
waits for owned goroutines to finish. Message storage remains application-owned
and can still be inspected according to its implementation contract.
