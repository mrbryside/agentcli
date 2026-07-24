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
`agentruntime.ErrTurnInProgress`. Reusing a persisted turn ID returns
`agentruntime.ErrTurnExists`.

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
submitted root prompts. That queue is UI-owned, while child follow-ups use the
subagent mailbox in `SubagentStorage`.

Within one model step, several tool calls may execute concurrently. The runtime
waits for all calls in that step and persists their result messages in original
provider order before it makes the next model request.

## Completion admission

Low-level AgentRuntime integrations may configure `Config.CompletionGuard` to
inspect a defensive transcript snapshot after the provider's final output has
been persisted but before `RunCompleted` is committed:

```go
guard := func(ctx context.Context, attempt agentruntime.CompletionAttempt) (
    agentruntime.CompletionDecision,
    error,
) {
    if outcomeExists(attempt.TurnID, attempt.Messages) || attempt.RepairCount > 0 {
        return agentruntime.CompletionDecision{
            Action: agentruntime.CompletionProceed,
        }, nil
    }
    return agentruntime.CompletionDecision{
        Action: agentruntime.CompletionRetry,
        ContextReminders: []agentruntime.ContextReminder{{
            Content: "Report the existing outcome; do not repeat the work.",
        }},
    }, nil
}
```

The retry reminder is ephemeral and applies only to the next provider request.
An optional non-nil allowlist restricts that request and all of its follow-up
rounds. A reminder can ask a child to call `report_subagent_outcome`, but the
provider is free to return a normal assistant response instead.
Guard implementations own their retry policy; use `RepairCount` to keep it
bounded. AgentCLI applies this mechanism automatically to child sessions to
enforce up to three `report_subagent_outcome` repairs without re-running domain
tools. Required end-of-turn finalizers are satisfied only by the final
all-successful, all-`EndTurn` tool batch; an early or mixed continuing batch
must be finalized again. While a finalizer remains unsatisfied, normal rounds
request any available tool, and repair rounds narrow that to the missing
finalizer. Providers that ignore this request policy can still emit text, so
the bounded completion guard remains the compatibility fallback.
OpenAI-compatible adapters append a repair reminder as an ephemeral
user-role message when assistant output already ends the transcript, avoiding
provider rejection of multiple trailing assistant messages.
Root callback turns do not use a completion repair. A successful
`close_subagent`, or its first controlled lifecycle conflict, returns
`ContinueTurn`, so cleanup naturally starts another provider round for the
user-visible result. Repeating the same lifecycle conflict in that parent turn
ends the turn instead of allowing an unbounded close loop.

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
- trusted runtime messages used for events such as subagent callbacks

Provider SDK types are never stored. A model adapter transforms these domain
messages each time it creates a provider request.

Assistant and tool-call messages may also contain `Reasoning`. It remains
separate from `Content`, is present only when the provider exposed reasoning,
and lets a UI restore collapsed reasoning after a session or child view is
reopened. Model adapters do not merge it into ordinary assistant text.
