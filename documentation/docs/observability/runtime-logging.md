---
title: Runtime logging
sidebar_position: 2
---

# Runtime logging

Agentcli can write structured framework lifecycle records to stderr. Logging is
owned by the runtime, so applications do not need to duplicate model-round,
tool, response-scope, repair, or subagent lifecycle logs.

Runtime logging is separate from [Langfuse](langfuse.md). Console logs describe
agent execution; Langfuse exports model-call telemetry.

## Configure the project

Add the optional mapping to `.agentcli/config.yaml`:

```yaml
logging:
  enabled: true
  level: info
```

Omitting `logging` disables console records. When the mapping is present,
`enabled` defaults to `true` and `level` defaults to `info`.

| Level | Records |
| --- | --- |
| `debug` | Provider completion, tool request/result details, compaction details, response-scope details, callback-obligation cancellation after application-owned child close, and canonical assistant persistence. |
| `info` | Turn start/completion, response-scope start/end, subagent close lifecycle, and repair requests. |
| `warn` | Recoverable framework warnings. |
| `error` | Failed/interrupted turns, failed repairs, final delivery failures, and canonical transcript persistence failures. |

Selecting a level includes records at that severity and above.

## Repair records

When an output or completion guard requests another provider round, agentcli
emits `agent repair requested` with:

- `repair_type`: `output_guard` or `completion_guard`;
- `attempt`: one-based retry count;
- `provider_steps`: model rounds consumed by the turn;
- `tool_allowlist`: restricted completion tools, when configured.

Guard feedback and context reminders are intentionally omitted. A failed guard
emits `agent repair failed` followed by the terminal run failure.

Rejected assistant drafts remain available through retained run/provider events
for diagnostics, but they are not written to `MessageStorage` and are not sent
back to the model during repair.

## End-of-scope delivery

An `EndResponseScope` delivery tool can set:

```go
agentcli.Tool{
    Definition:                         definition,
    Handler:                            deliver,
    Trigger:                            agentcli.EndResponseScope,
    EndTurnOnSuccess:                   true,
    CanonicalAssistantMessageParameter: "message",
}
```

After `deliver` succeeds, the framework appends the declared string argument
as the canonical assistant transcript message. Debug logging records the
persistence without logging message content. Failed delivery or persistence is
an error record and does not create a false assistant response.

## Payload safety

Debug tool parameters and results are recursively redacted when field names
look like tokens, secrets, passwords, authorization values, or API keys.
Formatted payloads are size-bounded. Runtime logs never include:

- provider reasoning;
- output/completion guard feedback;
- repair context reminders;
- canonical assistant message content.

Application handlers remain responsible for avoiding sensitive values in
ordinary error strings and non-framework logs.

## Programmatic logger

Use `WithLogger` when the host application already owns a `*slog.Logger`:

```go
logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
    Level: slog.LevelDebug,
}))

agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithLogger(logger),
)
```

Option order matters: applying `WithLogger` after `WithProject` overrides the
project logger. Child agents share the selected root logger. Passing `nil`
disables logging.

## Terminal playground

The repository terminal playground uses the same project configuration:

```bash
cp .agentcli/config.example.yaml .agentcli/config.yaml
go run ./playground/terminal
```

Uncomment the `logging` example and choose a level. Runtime records go to stderr
while the Terminal UI continues to use stdout.
