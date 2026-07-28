---
title: LLM observability
sidebar_position: 1
---

# Observability

Agentcli provides two independent observability surfaces:

- [Runtime logging](runtime-logging.md) writes structured agent, tool,
  response-scope, repair, and delivery lifecycle records to stderr.
- [Langfuse](langfuse.md) exports model-call traces through OpenTelemetry.

Runtime logging belongs to the execution framework. Langfuse instrumentation
wraps the provider-neutral `agentruntime.Model` boundary and keeps each
observation open until its stream completes or fails.

The current Langfuse scope is deliberately narrow:

- one generation observation per LLM call;
- main-agent, subagent, prompt-guard, tool-call guard, and compaction calls;
- session and turn correlation;
- provider/model labels, latency, first-output time, finish reason, and errors;
- optional prompt, response, and reasoning capture.

Tool handlers, permission checks, confirmations, storage operations, and other
runtime events are not traced. A tool loop that calls the model several times
therefore creates several generation traces. They are grouped by the runtime
session ID in the backend rather than nested below an agent-run root span.

## Provider-neutral model boundary

The observability decorator receives `ModelRequest`, starts an OpenTelemetry
client span, and passes the resulting context to the configured model. A
background subscriber observes the same replayable model stream as the
runtime. It records the first generated event and closes the span exactly once
on `StreamCompleted`, `StreamFailed`, startup failure, or cancellation.

Optional model capabilities such as `ModelMetadataProvider` and
`ContextEstimatorProvider` are preserved by the decorator. Enabling
observability therefore does not change context-compaction behavior.

## Session correlation

Each generation uses:

```text
langfuse.session.id                      = ModelRequest.SessionID
langfuse.observation.metadata.turn_id    = ModelRequest.TurnID
langfuse.observation.metadata.provider   = configured provider profile
langfuse.observation.model.name          = configured model
```

Prompt guards and compaction requests carry the same session and turn IDs as
the initiating run. Subagents have their own runtime session IDs and therefore
appear as separate sessions.

## Data and failure boundaries

Prompt, response, and reasoning payloads are disabled by default. Metadata and
timing remain available without them. Export happens asynchronously and an
export failure never changes a model result or runtime event.

The main `Agent` owns one exporter shared by its subagents. Call
`Agent.Close()` during graceful shutdown so queued observations are flushed.

For setup and field reference, see [Langfuse](langfuse.md).
