---
title: Context compaction
sidebar_position: 3
---

# Context compaction

Automatic context compaction keeps long-running sessions within a model's
context window without deleting their stored transcript. It summarizes an
older prefix into a cumulative checkpoint and sends the main model that
historical memory, a serialized recent-context suffix, and a recent native
message tail.

## Enable it

Add the optional strict mapping to `.agentcli/config.yaml`:

```yaml
compaction:
  auto: true
  provider: primary
  model: gpt-4.1-mini

providers:
  primary:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
    models:
      gpt-4.1-mini:
        context_window_tokens: 122880
        max_output_tokens: 66560
```

`provider` is an existing provider-profile alias and `model` selects the
separate summarizer. When present, `auto` defaults to `true`; omit the mapping
to disable compaction, or set `auto: false` to stop creating new checkpoints.

Optional limits are exact-model metadata, not tunable compaction budgets.
Main, subagent, and summarizer models therefore use their own matching entries,
even when they share one profile. The runtime derives its budgets from the
active main model's provider-neutral metadata.

The name under `providers.<alias>.models` must exactly match the `model` string
selected by `MAIN.md`, a subagent, or the `compaction` mapping. The `models`
mapping is not an allowlist; models without an entry continue through metadata
discovery and deterministic defaults.

The model's output limit remains capability metadata. For compacted main-model
rounds, the runtime reserves one eighth of the main model's context window,
capped at 16,384 tokens and reduced further by a lower explicit request limit
or lower model capability. The same value is sent to the provider. The runtime
then reserves a bounded summary and safety margin, estimates system prompts,
reminders, and tool schemas. The checkpoint can retain up to 8,192 tokens of
serialized recent context. The runtime also targets a recent native message
tail equal to one quarter of usable input clamped between 2,048 and 8,192
tokens. System and tool base costs can reduce it further. The unused room is
intentional: subsequent tool rounds can add results without immediately
filling the context again.

## What happens before a model round

Immediately before each main-provider call, the runtime:

1. builds and estimates the complete provider-neutral request, including
   system prompts, reminders, messages, and tools;
2. returns the unchanged request when it fits;
3. otherwise chooses a recent tail on a legal conversation boundary while
   keeping each tool-call/result batch together;
4. if one active turn is itself too large, retries inside that turn and keeps
   the largest recent complete assistant/tool suffix;
5. serializes the excluded prefix, capping each tool output at 2,000
   characters, and keeps its newest bounded suffix as recent context;
6. asks the configured tool-free summarizer once to merge the older
   serialization, prior recent context, and previous cumulative summary;
7. persists a new append-only checkpoint; and
8. re-estimates the projected request before starting the main model.

Compaction never divides one checkpoint into sequential summarizer calls. If
the complete one-shot summarization request cannot fit the summarizer model's
context window, the run fails with
`agentruntime.ErrCompactionPromptTooLarge` before the summarizer starts.

The active-turn fallback handles research loops that accumulate many large
tool results before producing a final answer. It summarizes the older tool
rounds, keeps the latest complete tool-call/result batch verbatim, and inserts
a synthetic runtime continuation before retained assistant activity. The
provider therefore receives a valid conversation sequence instead of an
orphaned tool call.

If the latest complete assistant/tool unit alone exceeds the recent-tail
target, the runtime keeps that unit whole when it still fits the provider
input. Tool-call/result adjacency takes priority over the 8,192-token target.

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
Exact file paths and IDs are preserved when still relevant. The summarizer is
also told to remove stale, superseded, and unrelated completed details.

## Storage is append-only

Compaction never deletes or rewrites original messages. A checkpoint stores a
cumulative summary, readable serialized recent context, and the IDs defining
its covered prefix and native message tail. The full original transcript
remains available for auditing and storage-level recovery.

For a main-provider call, the stored checkpoint is projected into a temporary
message list:

```text
application system prompts
assistant: historical summary + serialized recent context
[runtime continuation when the tail begins inside an active turn]
verbatim messages from TailStartMessageID through the latest message
```

The covered older messages and checkpoint records are omitted from that
provider request only. They remain in storage. A resumed session continues
projecting its latest checkpoint even if `auto` is later disabled. The
checkpoint is explicitly labelled as historical data, not instructions;
application system prompts and newer verbatim messages take precedence.

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

Project-created subagents inherit the compaction model and any explicit
estimator override. Without an override, each subagent selects the estimator from
its own model. A subagent emits compaction events with its own session and
turn IDs; those events remain on the subagent run rather than being mixed into the
main agent run. The main agent still receives the normal subagent result.

## Models and provider-neutral sizing

When compaction is enabled, the main model must expose valid context-window and
output-limit metadata. A summarizer that implements the optional metadata
capability is validated as well.

Each provider profile may declare optional exact-name entries under `models`.
An entry's `context_window_tokens` and `max_output_tokens` take priority only
for that model. Unlisted model names remain valid and are checked against the
authenticated provider `/models` endpoint and then models.dev. The final
defaults are 122,880 context tokens and 66,560 output tokens, displayed as
`120k` and `65k`. Explicit invalid or incomplete limits still fail validation.

The default `GenericContextEstimator` works across providers and estimates all
generic request surfaces conservatively, including multilingual text and tool
schemas. It uses a three-ASCII-bytes-per-token baseline so JSON, code, and
denser tokenizers compact before the common four-byte approximation would. A
main model implementing `ContextEstimatorProvider` is selected automatically
per runtime. Applications can still force an estimator across main-agent and
subagent runtimes:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithContextEstimator(providerEstimator),
)
```

Model adapters can wrap `agentruntime.ErrContextWindowExceeded` when a
provider rejects an oversized request. With compaction enabled, the runtime
then force-compacts the transcript and retries that provider round once. This
fallback is provider-neutral; only the adapter translates its provider error
into the shared signal.

For a custom adapter that cannot use compaction-scoped config or discovery,
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
