# Langfuse LLM-call observability

Langfuse is code-configured through `WithLangfuse(LangfuseConfig)`. The root
aliases expose `observability/langfuse.Config` and `CaptureConfig` without
requiring applications to import the subpackage. `.agentcli/config.yaml` has
no `observability` field; strict project decoding rejects that removed path.

The option stores a defensive configuration copy. `Agent.New` constructs the
client after the Agent context exists. `observability/langfuse.New` validates
credentials, an absolute HTTP(S) `BaseURL`, `SampleRate` in `[0,1]`, and
Langfuse environment-name constraints. Applications read environment variables
before passing the code configuration; project loading no longer expands
Langfuse settings.

## Export and lifecycle

`observability/langfuse.Client` owns a private OpenTelemetry tracer provider
and a batched OTLP/HTTP exporter. It derives the traces endpoint by appending
`/api/public/otel/v1/traces` to the configured installation root, uses HTTP
Basic Auth built from the configured public/secret keys, and sends
`x-langfuse-ingestion-version: 4`.

The root `Agent` creates and owns at most one client when `WithLangfuse` is
present. Its subagents receive the same client through the private
`withSharedLangfuse` option and never shut it down. `Agent.Close` closes
subagents and the executor before giving the owner
five seconds to flush and shut down telemetry. Agent construction error paths
also shut down a newly owned client.

Do not set the process-global OpenTelemetry tracer provider. The private
provider accepts any main agent span context passed to a model call without
changing the embedding application's telemetry setup.

## Model decoration

`Client.ObserveModel` wraps the provider-neutral `agentruntime.Model`, not the
OpenAI SDK call. It is idempotent for the same client and preserves the
optional `ModelMetadataProvider` and `ContextEstimatorProvider` capabilities so
context compaction keeps the same validation and estimator behavior.

`ModelIdentityProvider` supplies provider-profile and model labels. The OpenAI
adapter implements it from its configured adapter values.

One `generation` client span starts for each `Model.Start`. A background
subscriber follows the replayable stream independently from the runtime
subscriber and ends the span exactly once after completion or failure. The
span records:

- `langfuse.session.id` from `ModelRequest.SessionID`;
- `turn_id` and provider profile as observation metadata;
- model name, release, environment, finish reason, latency, and status;
- completion-start time on the first content, reasoning, or tool-call event;
- input, output, and reasoning only when their capture flags are enabled.

Prompt-guard and compaction requests propagate the initiating session/turn IDs
into their `ModelRequest`, so their model calls participate in the same
correlation. Subagents retain their own session IDs.

The provider-neutral `StreamResult` does not currently carry token usage.
Consequently Langfuse generations do not yet include usage or calculated cost.

## Verification

Focused coverage lives in:

- `observability/langfuse/model_test.go` for attributes, capture policy,
  terminal errors, context propagation, capability preservation, and
  idempotence;
- `observability/langfuse/client_test.go` for OTLP path and Langfuse headers;
- root `observability_test.go` for the public option and Agent construction;
- `project_test.go` for rejection of the removed YAML field;
- `agentruntime/guardrail_test.go` and `agentruntime/compactor_test.go` for
  session/turn propagation.

The Docusaurus user guides are under
`documentation/docs/observability/`, with the navigation category declared in
`documentation/sidebars.js`.

Back to [observability/index.md](index.md).
