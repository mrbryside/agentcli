---
title: Runs, sessions, and turns
sidebar_position: 1
---

# Runs, sessions, and turns

## Identity model

- A **session ID** is supplied by your application and identifies one long-lived
  conversation transcript.
- A **turn ID** identifies one agent-loop execution inside that session.
- A **call ID** identifies one provider-requested tool invocation inside a turn.

If `Request.TurnID` is empty, the runtime generates a cryptographically random
ID with a `turn_` prefix. Supplying your own turn ID is useful for idempotent
API clients, but it must not already exist in the session.

For the common case, send a user message and consume the returned live stream:

```go
run, subscription, err := agent.SendMessage(
    ctx,
    "customer-42",
    "Summarize my previous request.",
)
if err != nil {
    return err
}

for event := range subscription.Events {
    if event.Type == agentcli.ProviderEventReceived &&
        event.ProviderEvent.Type == agentcli.ContentReceived {
        fmt.Print(event.ProviderEvent.Content)
    }
}
result, err := run.Result()
```

`SendMessage` generates the turn/message IDs and timestamp. It installs the
subscription before `RunStarted`, so a CLI or transport adapter can receive the
whole turn from its first event.

Use the lower-level request API when the caller needs its own turn ID:

```go
run, events, err := agent.StartSubscribed(ctx, agentruntime.Request{
    SessionID: "customer-42",
    TurnID:    "request-018f", // optional
    Message: agentruntime.Message{
        Type:    agentruntime.MessageTypeUser,
        Content: "Summarize my previous request.",
    },
})
```

## Concurrency rules

Different sessions execute independently and can run in parallel:

```go
runA, _, err := agent.StartSubscribed(ctx, requestA) // session-a
runB, _, err := agent.StartSubscribed(ctx, requestB) // session-b
```

A second active turn for the same session returns
`agentcli.ErrTurnInProgress`. Reusing a persisted turn ID returns
`agentcli.ErrTurnExists`.

That is the deliberate low-level `Agent.Start` contract. It prevents two
callers from reading the same transcript head and interleaving their messages.

## Server turn processing

The Echo server adds transport-level processing above the strict runtime:

```text
session-a: active turn → queued turn 1 → queued turn 2
session-b: active turn → queued turn 1
session-c: active turn
```

Each row advances independently. The first request for an idle session starts
immediately and returns `201 Created`. Later requests for that same session are
accepted into a FIFO and return `202 Accepted` with `status: "queued"` and a
`queue_position`. The default bound is 64 waiting turns per session.

This queue exists at the server boundary rather than inside AgentRuntime so
direct Go callers retain explicit admission control. It also means an HTTP
disconnect does not discard an already accepted turn.

Configure the bound:

```go
agent.RunServer(agentcli.WithServerTurnQueueLimit(32))
```

`GET /v1/sessions/{sessionID}/turns/{turnID}` exposes queued status. Opening its
SSE endpoint waits for admission and begins the normal retained/live stream as
soon as the prior turn finishes. Interrupting a queued turn removes it before
any model or tool executes.

An application can provide the same user-visible FIFO behavior for locally
submitted main-agent prompts. That queue is UI-owned, while subagent follow-ups use the
subagent mailbox in `SubagentStorage`.

Within one model step, several tool calls may execute concurrently. The runtime
waits for all calls in that step and persists their result messages in original
provider order before it makes the next model request.

## Completion admission

Low-level AgentRuntime integrations may configure `Config.CompletionGuard` to
inspect a defensive snapshot containing the provider's pending final output
before `RunCompleted` is committed:

```go
guard := func(ctx context.Context, attempt agentruntime.CompletionAttempt) (
    agentruntime.CompletionDecision,
    error,
) {
    if resultReportExists(attempt.TurnID, attempt.Messages) || attempt.RepairCount > 0 {
        return agentruntime.CompletionDecision{
            Action: agentruntime.CompletionProceed,
        }, nil
    }
    return agentruntime.CompletionDecision{
        Action: agentruntime.CompletionRetry,
        ContextReminders: []agentruntime.ContextReminder{{
            Content: "Report the existing result; do not repeat the work.",
        }},
    }, nil
}
```

The pending assistant candidate is not yet in `MessageStorage`. A retry
discards it, so it is absent from the next provider request and future session
history; it remains available through retained run/provider events for
diagnostics. Proceeding persists it immediately before `RunCompleted`.

The retry reminder is ephemeral and applies only to the next provider request.
An optional non-nil allowlist restricts that request and all of its follow-up
rounds. In v0.1, a subagent step-limit finalizer exposes no tools and accepts
one text-only final answer as `TaskStateIncomplete`; it does not ask a child to
call a reporting tool. The provider may return a normal assistant response
instead.
Guard implementations own their retry policy; use `RepairCount` to keep it
bounded. Task finalization does not re-run domain tools. A required `EndTurn`
trigger is satisfied by its latest successful
result in the current turn. An `EndResponseScope` trigger is satisfied only by
a handler executed from the final response-scope completion boundary; earlier
calls are successful skipped results that continue the model round. A later
failed attempt for the same tool makes it unsatisfied again. Trigger
timing is independent of `EndTurnOnSuccess`, which controls only whether a
successful batch becomes terminal. If the model attempts to finish while a trigger tool
remains unsatisfied, the completion guard starts a repair round with a reminder
naming every missing trigger tool and exposes only those missing trigger tools.
If an application completion guard also returns a bounded tool allowlist, its
tools are merged with the missing trigger tools. AgentRuntime does not send
provider-specific tool-choice directives. The bounded completion guard fails
the turn after three consecutive repair attempts without progress.
OpenAI-compatible adapters append repair reminders as ephemeral user-role
messages. Rejected assistant drafts never become repeated trailing transcript
messages.
Trusted results first try to join a compatible active run between provider
rounds. The runtime holds a pending-input barrier until the result is durably
appended; it never changes an in-flight provider request. Fallback result
turns participate in the originating response-scope barrier. Intermediate turns with pending results may complete without an
`EndResponseScope` call, and their assistant drafts are discarded. When the
last turn reaches completion with no pending result, runtime repair requests
the final `EndResponseScope` tools, closes unshared completed/failed subagents,
and executes those handlers. The first-action guard applies only to the human
main-agent turn, so a result continuation may execute a final handler on provider
step one. Incomplete subagents remain open. The model-facing
catalog has no destructive close tool. When the application closes a subagent
through Go, Terminal, or HTTP, the coordinator cancels that subagent's outstanding
unreserved result obligations and releases the scope's result barrier. A
terminal cancellation marker also prevents a racing registration or rejected
result reservation from recreating an obligation. Close does not synthesize
a provider continuation; final handlers still require an active turn to reach
the completion boundary. See
[Subagent lifecycle control](../capabilities/subagent-lifecycle-control.md).

`Agent.SubscribeScopeEvents(ctx)` exposes two live-only boundaries for
each human response scope:

- `PreEndScope` (`pre_end_scope`) fires at the final completion boundary,
  before final `EndResponseScope`
  handlers.
- `EndScope` (`end_scope`) fires after cleanup and all final handlers run and
  the scope has been removed.

Each event carries the session, response scope ID, turn that made the scope
ready to end, touched subagent IDs, final tool names, and occurrence time.

## Run status

`run.Status()` reports `active`, `waiting_for_permission`,
`waiting_for_confirmation`, or `done`. Completion, failure, and interruption
are distinguished by final events and `Result()`, not separate status
strings. Treat events as the authoritative transition stream and status as a
current snapshot.

`run.Done()` reports whether the final event has been committed.
`run.Result()` is safe after completion and returns the folded provider-neutral
result. Before completion it reports that the run is not finished.

## Interruption

Interrupt the exact run:

```go
err := run.Interrupt(ctx, "cancelled from UI")
```

Interruption cancels the provider stream, cancels pending permission and
confirmation gates, sends a turn-scoped interrupt to active tool handlers, and
commits an interruption event. Other sessions continue.

Handlers must honor `ctx.Done()` for prompt cancellation:

```go
func execute(ctx context.Context, input jobInput) (jobOutput, error) {
    select {
    case value := <-performJob(input):
        return value, nil
    case <-ctx.Done():
        return jobOutput{}, ctx.Err()
    }
}
```

## Stored transcript

Retrieve the provider-neutral message snapshot independently from events:

```go
messages, err := agent.ListMessages(ctx, "customer-42")
```

The transcript contains all of these message types:

- `user`
- `assistant`
- `tool_call`
- `tool_result`
- trusted runtime messages used for events such as subagent results

Provider SDK types are never stored. A model adapter transforms these domain
messages each time it creates a provider request.

Assistant and tool-call messages may also contain `Reasoning`. It remains
separate from `Content`, is present only when the provider exposed reasoning,
and lets a UI restore collapsed reasoning after a session or subagent view is
reopened. Model adapters do not merge it into ordinary assistant text.
