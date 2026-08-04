---
title: Logging & Observability
sidebar_position: 4
---

# Logging and Observability

AgentCLI has two independent surfaces:

- runtime logging for agent, tool, repair, and response-scope lifecycle;
- optional Langfuse traces for model calls.

## Runtime logging

```yaml title=".agentcli/config.yaml"
logging:
  enabled: true
  level: info
```

Omit `logging` to disable it. A present mapping defaults to `enabled: true`
and `level: info`.

| Level | Typical records |
| --- | --- |
| `debug` | Provider, tool, compaction, and response-scope details. |
| `info` | Turns, response scopes, task closure, and repair requests. |
| `warn` | Recoverable framework warnings. |
| `error` | Failed/interrupted turns, repairs, and delivery. |

Records normally go to stderr. An interactive Terminal captures project logs
and logs enabled with `WithLogLevel`; use `Ctrl+L` or `/logs` to view them
without interrupting the conversation. A custom `WithLogger` remains
caller-owned and is not captured.

Debug tool payloads redact credential-like fields and truncate large values.
Runtime logs never include provider reasoning, guard feedback, or repair
reminders. Application tools must still avoid secrets in ordinary errors and
custom logs.

## Langfuse

Langfuse uses its OpenTelemetry OTLP/HTTP endpoint and does not replace the
application's global tracer provider.

```yaml title=".agentcli/config.yaml"
observability:
  langfuse:
    enabled: true
    base_url: ${LANGFUSE_BASE_URL}
    public_key: ${LANGFUSE_PUBLIC_KEY}
    secret_key: ${LANGFUSE_SECRET_KEY}
    environment: production
    service_name: agentcli
    release: ${APP_VERSION}
    sample_rate: 1.0
    capture:
      input: false
      output: false
      reasoning: false
```

`base_url` defaults to `https://cloud.langfuse.com`; select the correct Cloud
region or self-hosted root. Enabled configurations require matching public and
secret keys. `sample_rate` accepts `0` through `1`.

Input, output, and reasoning capture default to `false`. Enable them only when
the project's retention and data-residency policy permits sending those
payloads to Langfuse.

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
