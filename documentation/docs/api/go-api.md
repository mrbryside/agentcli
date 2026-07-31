---
title: Go API
sidebar_position: 3
---

# Go API

## Task API (v0.1)

`TaskResult` has `TaskID`, `AgentName`, `State`, `Output`, and `Error`; states
are `running`, `completed`, `incomplete`, and `error`. Use
`WithTaskForegroundWait(d)` to promote a foreground task after a positive wait
(`0` waits normally; negative is rejected). Subscribe to
`Agent.SubscribeSystemEvents` for `SystemTaskCompleted`; its metadata is
application-only.

`completed` means the child finished its current work; its `Output` may still
be a question for essential user input before a safe action can occur. Present
that question unchanged. When the user replies, the main model resumes the
same task with only `task_id` and `prompt`; it must not start a replacement
task. Independent task calls issued in the same tool-call batch run in
parallel, so two independent readers are two calls in that one batch.

The Agent owns background delivery. The exported result subscription,
injection, and continuation compatibility APIs referenced later in this legacy
reference are removed. Retained `StartSubagent`, `ListSubagents`, messaging,
run, interruption, permission/confirmation, and close APIs are host-only
child-session controls, not model-facing task compatibility.

This page summarizes the high-level Go surface. Detailed behavior is covered in
the feature guides.

## Project

```go
project, err := agentcli.LoadProject(root)
project, err := agentcli.LoadProjectContext(ctx, root)
```

Use `LoadProjectContext` when the caller needs to bound or cancel startup model
metadata discovery. `LoadProject` applies the library's bounded default.

Useful immutable accessors:

```go
project.Root()
project.ProviderName()
project.ModelName()
project.PermissionMode()
project.MainAgent()
project.Skills()
project.Subagents()
project.ToolNames()
project.SystemPrompts()
project.Model()
project.ModelFor(providerName, modelName)
project.Compaction()
project.CompactionModel()
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
| `WithCompactionModel` | Override the project summarizer or supply the tool-free compaction model programmatically. |
| `WithContextEstimator` | Replace conservative generic token estimation with a provider-aware `agentruntime.ContextEstimator`. |
| `WithTool` | Register an application-owned `agentcli.Tool`. |
| `WithPermissionMode` | Set initial mode. |
| `WithPermissionPolicy` | Supply explicit capability rules. |
| `WithNonInteractive` | Independent unattended-run flag: convert permission `ask` to `deny` and decline confirmations without changing permission mode. |
| `WithToolWorkers` | Set handler worker concurrency; default 4. |
| `WithToolCallGuardTimeout` | Bound each tool-call guard; default 30 seconds. |
| `WithChannelBuffer` | Set internal transport buffer; default 64. |
| `WithInputGuard` | Validate or replace normalized input with application code before persistence. |
| `WithOutputGuard` | Validate final assistant output and return repair feedback. |
| `WithInputGuardPrompt` | Evaluate input with a policy prompt and the main model by default. |
| `WithOutputGuardPrompt` | Evaluate assistant output with a policy prompt and the main model by default. |
| `WithInputGuardProvider` | Select a loaded project provider profile and model for the input prompt guard. |
| `WithOutputGuardProvider` | Select a loaded project provider profile and model for the output prompt guard. |
| `WithMessageStorage` | Replace transcript storage. |
| `WithPermissionStorage` | Replace permission/grant storage. |
| `WithConfirmationStorage` | Replace confirmation storage. |
| `WithSubagentStorage` | Replace subagent relationship storage. |
| `WithProviderStepLimit` | Opt into an agentic provider-round budget; omission is unlimited, while exhaustion starts restricted finalization with only required completion tools. |
| `WithSystemPrompt` | Add ephemeral provider instructions. |
| `WithContextReminderProvider` | Add trusted per-round context not persisted in messages. |

### Custom compaction adapters

`WithModel` and `WithCompactionModel` can override the model selected by a
project. Explicit limits belong to exact-name model entries under a provider
profile, allowing main, subagent, and summarizer models to use independent
metadata even when they share a connection.
Without explicit limits, project loading checks provider `/models`, then
models.dev, and finally uses exact defaults of 122,880 context tokens and
66,560 output tokens. For a fully custom adapter, build it with
`openai.Config.MetadataResolver`, then pass that adapter to the appropriate
option. The resolver supplies provider-neutral context-window and output
limits; compaction refuses to guess those limits. A custom model may implement
`ContextEstimatorProvider` to select its estimator automatically. Use
`WithContextEstimator` only for an explicit application-wide override.

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

## Application tools

| API | Purpose |
| --- | --- |
| `WithTool(tool)` | Register one application-defined tool. |
| `Tool` | Definition, handler, trigger or optional end-on-success behavior, call guard, and admission metadata. |
| `ToolDefinition` | Model-facing name, description, and input schema. |
| `ObjectSchema(parameters)` | Build a closed object schema. |
| `TryObjectSchema(parameters)` | Build a schema without panic. |
| `InputSchema` | Typed JSON Schema vocabulary. |
| `RawInputSchema(raw)` | Validate an advanced raw object schema. |
| `DecodeArguments(raw, target)` | Strictly decode one JSON object. |
| `ToolStaticPermission(config)` | Build a static permission descriptor. |
| `EndTurn` | Require a tool at turn completion and run it immediately. |
| `EndResponseScope` | Require a tool at the final response-scope completion repair; earlier calls are successful non-executing skips. |
| `Tool.EndTurnOnSuccess` | End the current turn after the full tool batch succeeds, independently of `Trigger`. |
| `Tool.RequiredSkills` | Require successful current-turn `load_skill` results before the handler may execute. |
| `RequestEndTurn(ctx)` | Conditionally request turn termination from inside a handler; applies only when the handler and full tool batch succeed. |
| `Tool.ResponseScopeCallLimit` | Set a hard cumulative call budget shared by all turns in one response scope. |
| `ToolCallGuard` | Function result for validating a requested tool call before execution. |
| `ToolCallGuardPrompt` | `Tool` field containing a model-evaluated call policy. |
| `GuardModelConfig` | Optional provider/model selection for one prompt-backed tool guard. |
| `ToolCallAllow`, `ToolCallReject` | Select the tool-call verdict. |

`Tool` fields are `Definition`, `Handler`, `Trigger`, `EndTurnOnSuccess`,
`RequiredSkills`,
`ResponseScopeCallLimit`,
`ToolCallGuard`, `ToolCallGuardPrompt`, `ToolCallGuardModel`, `Permission`,
`PermissionWithPolicy`, and
`Confirmation`. `ToolCallGuardModel` optionally holds one
`GuardModelConfig` for prompt-guarded tools; without it the guard uses the
Agent model. The schema helpers cover string, integer, number, boolean, null,
object, and array parameters with individual descriptions and constraints.

The zero trigger executes immediately and continues normally.
`Trigger: EndTurn` is a required per-turn trigger tool.
`Trigger: EndResponseScope` is a required response-scope trigger tool.
Neither trigger ends the turn by itself. `EndTurnOnSuccess: true` independently
ends the turn after every call in its batch succeeds. Without a trigger the
tool remains optional and immediate; with `EndTurn` it is required and
immediate; with `EndResponseScope` it is required only at the final
response-scope boundary. It can share a
batch with normal tools and cannot bypass missing required triggers.
When a tool is registered, agentcli automatically appends runtime timing
guidance to the cloned `ToolDefinition.Description` for `EndTurn`,
`EndResponseScope`, and `EndTurnOnSuccess`. This supplements the
application-written description with when the model should call the tool,
whether its handler runs immediately, the exact early-skip semantics, and
whether a successful batch ends the turn. Registration does not mutate the
caller's original definition.

When `RequiredSkills` is non-empty, every named skill must be available to the
current agent and loaded successfully in the current turn before the handler
can execute. Both the full `instructions` result and
`instructions_in_context=true` satisfy a `status=loaded` result. A successful
load returns `tools_unchanged=true`: it changes instruction context only and
does not add, remove, or enable tools listed with the current request. A
successful load from an earlier turn does not satisfy the requirement. A blocked call
returns `executed=false`, `reason=required_skill_not_loaded`, the complete
`required_skills` list, and the remaining `missing_skills`; it bypasses
admission, call budgets, guards, and the handler. Registration appends the same
requirement to the cloned model-facing tool description.

For invocation-specific behavior, a handler may call `RequestEndTurn(ctx)`.
This has the same successful-batch semantics as `EndTurnOnSuccess`, but only
for the invocation whose handler requested it. Handler failure or interruption
discards the request.
Missing trigger tools use bounded repair
rounds with a reminder naming the missing tools and a tool allowlist containing
only those trigger tools. A caller-supplied completion guard may add its own
bounded allowlist entries.

That provider-facing tool set is enforced at runtime before dispatch. Calls to
registered tools omitted from the current request are not sent to the executor.
Malformed/truncated tool arguments and calls for tools absent from that exact
request receive one tools-disabled text recovery round and are never
automatically replayed. The failed call emits no `ToolCallRequested` event, and
the recovery request is included in `RunResult.Steps`.

When `ResponseScopeCallLimit` is positive, admitted calls share one counter
across the main-agent turn and all inline/result continuation work. Exhaustion
returns a successful non-executing
`reason=response_scope_tool_budget_exhausted` result without admission or
handler execution. If the budgeted tool is an `EndResponseScope` trigger, the
runtime also records `trigger_satisfied=false`, so a controlled skip cannot
satisfy required-tool completion repair.

For a response-delivery tool whose successful execution should also finish the
current turn, set `Trigger: agentcli.EndResponseScope` and
`EndTurnOnSuccess: true`. Calls made as the human main-agent turn's initial provider action or
while the scope is busy return `status=succeeded`, `action=skipped`,
`executed=false`, `reason=tool_called_at_wrong_time`, and
`trigger_satisfied=false`.
They do not invoke admission, the tool-call guard, or the handler, and do not
retain a candidate for later execution. The result tells the model to treat
the call as success and not retry it. The injected tool description tells the
model to continue the remaining work until runtime completion repair requests
the final call. Without
`EndTurnOnSuccess` the provider continues. With it, a successful
skipped result ends the current turn only when results or other active turns
keep the response scope open; if the scope is otherwise quiescent, a premature
call continues so the model can finish ordinary work. For compatibility, the
coordinator still accepts a later provider-round main-agent call once the scope has
no pending results, but this is not the model-facing retry path. Result
continuation turns may execute the tool on provider step one. A normal
completion attempt makes runtime repair expose the required tool. Successful
delivery remains in the
durable transcript as its tool-call and tool-result records; the runtime does
not create an additional assistant message from the tool arguments.

Use `agent.SubscribeScopeEvents(ctx)` for live-only scope boundaries.
`agentcli.PreEndScope` is emitted when the last turn reaches the final boundary
but before cleanup and final handlers. `agentcli.EndScope` is emitted after
cleanup, final handler invocation, and scope removal. Both events include
`SessionID`, initiating `ScopeID`, `TriggerTurnID`, `SubagentIDs`, `ToolNames`, and
`OccurredAt`.

## Guardrails

The root package exposes the result, attempt, decision, and action types for
input, assistant output, and tool calls. Function and prompt modes are
mutually exclusive at the same boundary. Prompt verdicts are strict JSON and
fail closed.

Result `InputReject` returns an error matching
`agentcli.ErrInputGuardRejected` before a `Run` exists. Result
`InputRespond`, and rejected input prompt verdicts, return a completed streamed
turn without calling the main model. Assistant-output rejection requests
another provider round with ephemeral feedback. Tool-call rejection publishes
a failed tool result so the agent can decide whether and how to call the tool
again.

See [Guardrails overview](../guardrails/overview.md) for lifecycle and security
details.

## Turns

```go
run, subscription, err := agent.SendMessage(ctx, sessionID, message)
run, err := agent.Start(ctx, request)
run, subscription, err := agent.StartSubscribed(ctx, request)
```

`SendMessage` is the ordinary application-facing path. It creates a user
message, generates its turn/message IDs and timestamp, and installs the live
subscription before `RunStarted`. Reuse the same session ID to continue its
stored conversation. Use `Start` or `StartSubscribed` when you need to supply a
turn ID or a trusted runtime-event message explicitly.

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
agent.StartSubagent(ctx, mainAgentSessionID, mainAgentTurnID, name, message, label)
agent.SendSubagentMessage(ctx, mainAgentSessionID, subagentID, message)
agent.ListSubagents(ctx, mainAgentSessionID, includeClosed)
agent.CloseSubagent(ctx, mainAgentSessionID, subagentID)
agent.InterruptSubagent(ctx, mainAgentSessionID, subagentID, reason)
agent.SubscribeSystemEvents(ctx)
agent.SubscribeSubagentPermissions(ctx)
agent.PendingSubagentPermissions(ctx, mainAgentSessionID)
agent.SubscribeSubagentConfirmations(ctx)
agent.PendingSubagentConfirmations(ctx, mainAgentSessionID)
agent.ReadSubagent(ctx, mainAgentSessionID, subagentID, afterMessageID)
agent.WaitSubagent(ctx, mainAgentSessionID, subagentIDs, afterVersions)
```

The task completion type uses explicit child identity:

```go
agentcli.TaskCompletedEvent{
    TaskID:            "...",
    SubagentSessionID: "...",
    SubagentTurnID:    "...",
    AgentName:         "researcher",
    State:             agentcli.TaskStateCompleted,
}
```

`SubagentReadResult.LastMessageID` is the durable observation cursor returned
by `ReadSubagent`. `SubagentConfirmationEvent` and
`SubagentPermissionEvent` expose `DefinitionName`, `SubagentSessionID`, and
`SubagentTurnID`. `SystemEvent` exposes `MainAgentSessionID` and
`MainAgentTurnID`; no legacy identity aliases are retained.

`SystemTaskCompleted` identifies each terminal task and carries any validated
application-only result-contract metadata. Agent owns delivery; applications
do not subscribe to raw task results or create fallback continuation turns.

`CloseSubagent` is an explicit destructive command: it may interrupt active
work, drops queued subagent input, cancels outstanding unreserved result
obligations, retains transcript/run history, and rejects future sends. The
result cancellation releases the main agent response scope's result barrier.
Bind it to a direct user action. It is idempotent and normal task completion
does not call it. Completed, incomplete, and failed runs all remain resumable
by exact task ID until explicitly closed.
Cancellation is terminal for the closed subagent, so racing assignment registration
and result-reservation rollback cannot recreate the obligation. It releases
scope accounting but does not create a provider turn. See
[Subagent lifecycle control](../capabilities/subagent-lifecycle-control.md).
`SubscribeSystemEvents` reports agent-level facts that are not owned by one
runtime turn. `SystemSubagentClosed` reports the first successful explicit
close with the final subagent snapshot and close effects. The
previous structured result field is `PreviousResultStatus`.

Subagent decision methods require main agent and subagent ownership in addition to the
normal correlated decision:

```go
agent.ResolveSubagentPermission(ctx, mainAgentSessionID, subagentID, decision)
agent.ResolveSubagentConfirmation(ctx, mainAgentSessionID, subagentID, decision)
```

Standard subagents evaluate the main agent's permission policy and mode. Permission
and confirmation requests are sent to the main agent event stream and remain
recoverable through `PendingSubagentPermissions` and
`PendingSubagentConfirmations`; the main agent session UI, not a subagent UI or the
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
`WithServerMiddleware`. The queue option bounds accepted waiting main-agent
turns per session; it does not change direct `Agent.Start` admission.

## Lifecycle

Always close an agent:

```go
defer agent.Close()
```

Close cancels active runs and the executor, closes subagent management, and
waits for owned goroutines to finish. Message storage remains application-owned
and can still be inspected according to its implementation contract.
