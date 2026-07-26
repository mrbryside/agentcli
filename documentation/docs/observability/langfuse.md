---
title: Langfuse
sidebar_position: 2
---

# Langfuse

Langfuse support uses its OpenTelemetry OTLP/HTTP ingestion endpoint. No
Langfuse-specific SDK is required, and agentcli does not replace the host
application's global OpenTelemetry tracer provider.

## Configure the project

Add the optional mapping to `.agentcli/config.yaml`:

```yaml
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
      input: true
      output: true
      reasoning: false
```

Set credentials in the process environment:

```bash
export LANGFUSE_BASE_URL='https://cloud.langfuse.com'
export LANGFUSE_PUBLIC_KEY='pk-lf-...'
export LANGFUSE_SECRET_KEY='sk-lf-...'
export APP_VERSION='dev'
```

Choose the base URL for the Langfuse Cloud region that owns the project, or the
root URL of a self-hosted installation. When omitted, `base_url` defaults to
`https://cloud.langfuse.com`. Agentcli appends
`/api/public/otel/v1/traces`, constructs HTTP Basic Auth from the public and
secret keys, and sends `x-langfuse-ingestion-version: 4`.

## Fields

| Field | Required | Behavior |
| --- | --- | --- |
| `enabled` | No | Defaults to `false`. A missing or disabled mapping creates no exporter. |
| `base_url` | No | Langfuse installation root; defaults to the EU Cloud URL. |
| `public_key` | When enabled | Langfuse project public key. |
| `secret_key` | When enabled | Langfuse project secret key. |
| `environment` | No | Lowercase environment label, up to 40 characters, and must not start with `langfuse`. |
| `service_name` | No | OpenTelemetry service name; defaults to `agentcli`. |
| `release` | No | Application release attached to every generation. |
| `sample_rate` | No | Number from `0` through `1`; defaults to `1`. |
| `capture.input` | No | Captures system prompts, messages, reminders, token limit, and tool schemas. |
| `capture.output` | No | Captures assistant content and generated tool calls. |
| `capture.reasoning` | No | Captures provider reasoning separately from normal output. |

The session ID, turn ID, provider profile, and model name come from the model
request and project definitions. Do not duplicate them in YAML.

## Privacy

`capture.input`, `capture.output`, and `capture.reasoning` all default to
`false`. Enabling input capture may send system instructions, conversation
history, context reminders, and tool schemas to Langfuse. Enable payloads only
when the project's retention, access, and data-residency policies permit it.

Provider API keys and Langfuse credentials are never attached to spans. Keep
all live credentials in environment variables rather than committing them to
the project file.

## What appears in Langfuse

Each LLM call is a generation and, with the current LLM-only scope, also acts
as its own trace. Calls sharing a runtime `SessionID` appear under one Langfuse
session. Calls from the same turn can be filtered using the `turn_id`
observation metadata.

Agentcli currently does not populate token usage or model cost because the
provider-neutral `StreamResult` does not expose usage yet. Prompt/response,
latency, first-output time, finish reason, model labels, correlation, and error
status are available.

## Shutdown and troubleshooting

Always close the root Agent:

```go
agent, err := agentcli.New(ctx, agentcli.WithProject(project))
if err != nil {
    return err
}
defer agent.Close()
```

`Agent.Close()` closes live children, stops the executor, and then gives the
shared exporter up to five seconds to flush. Short-lived programs that exit
without closing the Agent may lose queued spans.

If no generation appears:

1. Confirm `enabled: true` and a non-zero `sample_rate`.
2. Confirm the public and secret keys belong to the same Langfuse project.
3. Confirm `base_url` selects the correct Cloud region or self-hosted host.
4. Trigger a real model call; tool-only and rejected-before-model flows create
   no generation.
5. Close the Agent and inspect shutdown errors.

Project loading rejects missing enabled credentials, malformed HTTP(S) base
URLs, invalid sample rates, invalid environments, and unknown YAML fields
before the first model request.
