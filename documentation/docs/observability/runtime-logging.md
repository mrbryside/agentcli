---
title: Runtime logging
sidebar_position: 2
---

# Runtime logging

Agentcli can write structured framework lifecycle records. They normally go to
stderr. During an interactive Terminal UI session, project-managed records are
captured in a dedicated view instead, so they do not interrupt the prompt or a
streaming response. Logging is owned by the runtime, so applications do not
need to duplicate model-round, tool, response-scope, repair, or subagent
lifecycle logs.

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
| `debug` | Provider completion, tool request/result details, compaction details, response-scope details, and result-obligation cancellation after application-owned subagent close. |
| `info` | Turn start/completion, response-scope start/end, subagent close lifecycle, and repair requests. |
| `warn` | Recoverable framework warnings. |
| `error` | Failed/interrupted turns, failed repairs, and final delivery failures. |

Selecting a level includes records at that severity and above.

## Repair records

When a guard or recoverable provider response requests another provider round,
agentcli emits `agent repair requested` with:

- `repair_type`: `output_guard`, `completion_guard`, or `provider_response`;
- `attempt`: one-based retry count;
- `provider_steps`: model rounds consumed by the turn;
- `tool_allowlist`: restricted completion tools, or an empty list for
  `provider_response`.

`provider_response` is one bounded, tools-disabled text round after
malformed/truncated tool arguments or a completed call for a tool absent from
that exact provider request. The invalid call is not executed or replayed. If
the recovery response is also invalid, the run fails instead of requesting a
second recovery.

Guard feedback and context reminders are intentionally omitted. A failed guard
emits `agent repair failed`; an exhausted provider-response recovery proceeds
directly to the terminal run failure record.

Rejected assistant drafts remain available through retained run/provider events
for diagnostics, but they are not written to `MessageStorage` and are not sent
back to the model during repair.

## End-of-scope delivery

An `EndResponseScope` delivery tool can set:

```go
agentcli.Tool{
    Definition:       definition,
    Handler:          deliver,
    Trigger:          agentcli.EndResponseScope,
    EndTurnOnSuccess: true,
}
```

After `deliver` succeeds, the durable tool call and tool result record the
delivery. The framework does not append a synthetic assistant message from the
tool arguments.

## Payload safety

Debug tool parameters and results are recursively redacted when field names
look like tokens, secrets, passwords, authorization values, or API keys.
Formatted payloads are size-bounded. Runtime logs never include:

- provider reasoning;
- output/completion guard feedback;
- guard and provider-response repair context reminders.

Application handlers remain responsible for avoiding sensitive values in
ordinary error strings and non-framework logs.

## Programmatic logger

Use `WithLogLevel` for the same Terminal-aware routing without project logging
configuration:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithLogLevel(slog.LevelDebug),
)
```

These records go to stderr outside an interactive Terminal and move into the
runtime-log view while it is attached.

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
project logger. Subagents share the selected main-agent logger. A
caller-supplied handler keeps its caller-owned routing and is not captured by
the Terminal UI.

## Terminal playground

The repository terminal playground enables managed debug logging with
`WithLogLevel`:

```bash
cp .agentcli/config.example.yaml .agentcli/config.yaml
go run ./playground/terminal
```

In an interactive Terminal, press `Ctrl+L` (or enter `/logs`) to open or close the runtime-log view. It
retains up to 2,000 recent records and follows new records while the agent runs.
Non-interactive terminal runs continue to write records to stderr.
