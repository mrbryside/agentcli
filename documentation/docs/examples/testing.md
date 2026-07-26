---
title: Testing integrations
sidebar_position: 2
---

# Testing integrations

Test custom handlers independently, then test one full agent turn with a local
scripted model. Live provider tests should be a separate opt-in layer.

## Raw tool unit test

```go
func TestLookupTool(t *testing.T) {
    tool := newLookupTool()

    output, err := tool.Handler(
        context.Background(),
        json.RawMessage(`{"topic":"Go"}`),
    )
    if err != nil {
        t.Fatal(err)
    }
    if string(output) != `{"topic":"Go"}` {
        t.Fatalf("output = %s", output)
    }

    if _, err := tool.Handler(
        context.Background(),
        json.RawMessage(`{"topic":"Go","unknown":true}`),
    ); err == nil {
        t.Fatal("unknown field accepted")
    }
}
```

Also inspect `tool.Definition.InputSchema` so explicit schema and parameter
description regressions are visible. Test non-object input, unknown fields,
multiple values, malformed JSON, and missing required values separately.

For context-aware tools, attach deterministic invocation metadata with
`agentcli.WithToolInvocation`.

## Permission test

For every permission mode and risk combination, assert whether the handler:

- runs automatically;
- emits a correlated request and waits;
- returns a denied tool result.

When a request waits, verify the handler has not started before the decision.
Resolve with exact request IDs and assert resolution appears before the tool
result.

## Confirmation test

Cover both answers:

```text
Yes -> handler runs once -> succeeded result
No  -> handler never runs -> declined result
```

Also test interruption, late answers, non-interactive mode, simultaneous
sessions, and a tool configured with both permission and confirmation.

The `playground/terminal` package's `confirm_demo` is a complete executable
example. Its colocated tests cover output encoding, unknown fields, bounded
display input, and cancelled contexts.

## Required trigger tool test

Use a scripted provider to return text without the required tool, then assert
the repair request contains a reminder naming the missing trigger tool while
restricting the tool catalog to missing trigger tools. Return a successful
standalone call and assert that the provider receives another round unless
`EndTurnOnSuccess` is set. Also cover three
consecutive no-progress repairs, multiple trigger tools, mixed continuing batches,
latest-attempt failure, and progress resetting the repair budget.

For `EndTurnOnSuccess`, cover both standalone optional tools and tools that also
set `EndTurn` or `EndResponseScope`. Assert that a successful mixed batch ends
without another provider request, any failed result continues, and a missing
required trigger still causes a bounded repair. For `EndResponseScope`,
explicitly test the first-tool-call case: the early call must return successful
`status=skipped` with `executed=false`, bypass admission and the handler, ignore
`EndTurnOnSuccess`, and continue the turn. After ordinary work and a natural
completion attempt, assert that the restricted completion-repair call executes
the handler exactly once and satisfies the trigger.

## HTTP test

Construct without a network listener:

```go
server, err := agentcli.NewServer(agent,
    agentcli.WithServerHeartbeat(time.Millisecond),
)
if err != nil {
    t.Fatal(err)
}
httpServer := httptest.NewServer(server.Handler())
defer httpServer.Close()
```

Test this sequence:

1. `POST` a turn and save its IDs.
2. Connect to SSE and save each event ID.
3. Disconnect while permission/confirmation is pending.
4. Post the decision through the separate endpoint.
5. Reconnect using `Last-Event-ID`.
6. Assert no event is missing or duplicated.
7. Read `/messages` and verify tool-call/result correlation.

Add a separate turn-processing test:

1. Hold session A's first model stream open.
2. POST two more session A messages and assert `202`, `queued`, and positions
   1 then 2.
3. Start session B and assert it reaches the model immediately.
4. Release session A and verify FIFO transcript order.
5. Cancel a queued turn and verify the model and tools never receive it.
6. Fill the configured bound and verify `429 turn_queue_full`.

## Repository verification

```bash
go test ./...
go test -race ./...
go vet ./...

cd documentation
npm run build
```

The race run matters because sessions, subscribers, tool workers, decisions,
and child callbacks intentionally operate concurrently.
