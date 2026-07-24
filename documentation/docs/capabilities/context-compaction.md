---
title: Context compaction
sidebar_position: 3
---

# Context compaction

Automatic context compaction keeps long-running sessions within a model's
context window without deleting their stored transcript. It summarizes an
older prefix into a cumulative checkpoint and sends the main model that memory
plus a recent verbatim tail.

## Enable it

Add the optional strict mapping to `.agentcli/config.yaml`:

```yaml
compaction:
  auto: true
  provider: primary
  model: gpt-4.1-mini
```

`provider` is an existing provider-profile alias and `model` selects the
separate summarizer. The mapping accepts only `auto`, `provider`, and `model`.
When present, `auto` defaults to `true`; omit the mapping to disable
compaction, or set `auto: false` to stop creating new checkpoints.

Token budgets are intentionally not YAML settings. The runtime derives the
input reserve, recent tail, serialization limit, and summary limit from
provider-neutral model metadata.

## What happens before a model round

Immediately before each main-provider call, the runtime:

1. builds and estimates the complete provider-neutral request, including
   system prompts, reminders, messages, and tools;
2. returns the unchanged request when it fits;
3. otherwise chooses a recent tail on a legal conversation boundary while
   keeping each tool-call/result batch together;
4. asks the configured tool-free summarizer to merge the older prefix with any
   previous cumulative summary;
5. persists a new append-only checkpoint; and
6. re-estimates the projected request before starting the main model.

The summary uses the conversation's primary language and an anchored Markdown
schema:

```markdown
# Objective
# Important Details
# Work State
## Completed
## Active
## Blocked
# Next Move
# Relevant Files
```

Transcript history is treated as data rather than summarizer instructions.
Exact file paths and IDs are preserved.

## Storage is append-only

Compaction never deletes or rewrites original messages. A checkpoint stores a
cumulative summary and the IDs defining its covered prefix and recent tail.
The full original transcript remains available for auditing and storage-level
recovery.

For a main-provider call, the stored checkpoint is projected into a temporary
message list:

```text
application system prompts
system: cumulative conversation memory
verbatim messages from TailStartMessageID through the latest message
```

The covered older messages and checkpoint records are omitted from that
provider request only. They remain in storage. A resumed session continues
projecting its latest checkpoint even if `auto` is later disabled.

HTTP message endpoints expose `type: "compaction_checkpoint"` so clients can
preserve ordering, but omit the internal summary and boundary fields. Do not
render checkpoint records as assistant output.

## Lifecycle events

When a new checkpoint is required, the run emits:

| Event | Timing |
| --- | --- |
| `compaction_started` | Immediately before the separate summarizer starts. |
| `compaction_completed` | After the checkpoint is persisted and before the main provider starts. |
| `compaction_failed` | When preparation, summarization, or persistence fails. The main-provider round does not start. |

No compaction event is emitted when the request already fits or when the
runtime only projects an existing checkpoint.

Project-created subagents inherit the compaction model and estimator. A child
emits compaction events with its own session and turn IDs; those events remain
on the child run rather than being mixed into the parent run. The parent still
receives the normal subagent outcome callback.

## Models and provider-neutral sizing

When compaction is enabled, the main model must expose valid context-window and
output-limit metadata. A summarizer that implements the optional metadata
capability is validated as well. Unknown required limits fail startup rather
than being guessed.

Project provider profiles can declare limits once under
`models.<model-id>`. Explicit config has priority. For an unknown model without
config, startup checks the authenticated provider `/models` endpoint first and
falls back to `models.dev` only when the deployment supplies no valid limits.
If both sources fail, startup reports the provider/model and a YAML snippet to
add. This provider-scoped metadata is reused by main, compaction, and child
model construction.

The default `GenericContextEstimator` works across providers and estimates all
generic request surfaces conservatively, including multilingual text and tool
schemas. Applications with a provider-specific tokenizer can replace it:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithContextEstimator(providerEstimator),
)
```

For a custom adapter that cannot use provider-scoped config or discovery,
construct it with `openai.Config.MetadataResolver`, then override the
project-selected main model or summarizer:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithModel(mainAdapter),
    agentcli.WithCompactionModel(summaryAdapter),
)
```

See [Project configuration](../getting-started/project-configuration.md) for
the complete project file and [Events and history](../agentcli/events-and-history.md)
for subscription and recovery semantics.
