---
title: Logging & Observability
sidebar_position: 4
---

# Logging and Observability

AgentCLI has two independent surfaces:

- runtime logging for agent, tool, repair, and response-scope lifecycle;
- optional Langfuse traces for model calls.

## Runtime logging

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithLogLevel(agentcli.LevelInfo),
)
```

For project-driven setup, the equivalent configuration is:

```yaml title=".agentcli/config.yaml"
logging:
  enabled: true
  level: info
```

Omit both `WithLogLevel` and the `logging` mapping to disable managed runtime
logs. An option applied after `WithProject` takes precedence. Use `WithLogger`
when the application owns a custom `*slog.Logger` and its output routing.

| Level | Typical records |
| --- | --- |
| `debug` | Provider, tool, compaction, and response-scope details. |
| `info` | Turns, response scopes, task closure, and repair requests. |
| `warn` | Recoverable framework warnings. |
| `error` | Failed/interrupted turns, repairs, and delivery. |

Managed records normally go to stderr. An interactive Terminal captures them;
use `Ctrl+L` or `/logs` to view them without interrupting the conversation. A
custom `WithLogger` remains caller-owned and is not captured.

Debug tool payloads redact credential-like fields and truncate large values.
Runtime logs never include provider reasoning, guard feedback, or repair
reminders. Application tools must still avoid secrets in ordinary errors and
custom logs.

## Langfuse

Langfuse uses its OpenTelemetry OTLP/HTTP endpoint and does not replace the
application's global tracer provider.

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithLangfuse(agentcli.LangfuseConfig{
        BaseURL:     os.Getenv("LANGFUSE_BASE_URL"),
        PublicKey:   os.Getenv("LANGFUSE_PUBLIC_KEY"),
        SecretKey:   os.Getenv("LANGFUSE_SECRET_KEY"),
        Environment: "production",
        ServiceName: "agentcli",
        Release:     os.Getenv("APP_VERSION"),
        SampleRate:  1.0,
        Capture: agentcli.LangfuseCaptureConfig{
            Input:     false,
            Output:    false,
            Reasoning: false,
        },
    }),
)
```

Langfuse is not accepted in `.agentcli/config.yaml`. Omit `WithLangfuse` to
disable tracing. `BaseURL` defaults to
`https://cloud.langfuse.com`; select the correct Cloud region or self-hosted
root. Public and secret keys are required. `SampleRate` accepts `0` through
`1`; set it explicitly.

Input, output, and reasoning capture default to `false`. Enable them only when
the project's retention and data-residency policy permits sending those
payloads to Langfuse.

The Agent owns one exporter and shares it with project-created task agents.
Each model call becomes one generation, including main-agent, task-agent,
guard, and compaction calls. Session/turn IDs, provider/model labels, latency,
first-output time, finish reason, and error status are correlated. Tool
handlers and storage operations are not model traces.

AgentCLI currently does not report token usage or model cost because the
provider-neutral stream result does not expose usage.

## Shutdown

Always close the Agent:

```go
defer agent.Close()
```

Close stops live task agents and the executor, then gives the shared exporter
up to five seconds to flush. Telemetry export failure does not change model or
runtime results.
