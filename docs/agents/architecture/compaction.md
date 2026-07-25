# Context compaction

Read this file when changing automatic context sizing, summary generation,
checkpoint storage, request projection, compaction events, or child-agent
inheritance.

## Preflight and budgets

Compaction runs immediately before every main-model round. The runtime builds a
provider-neutral `ModelRequest`, estimates its full input, and compares it with
the main model's validated context-window and output-limit metadata. Internal
input, recent-tail, serialization, and summary budgets are derived in code;
only the shared model limits are configurable in project YAML.

The input budget reserves the model's maximum output. Summary and estimator
safety reserves each scale with the remaining input and cap at 4,096 tokens.
Recent history does not use a fixed percentage: the estimator first charges
system prompts, context reminders, tool schemas, the bounded summary
placeholder, and the safety reserve, then gives the complete remaining usable
input to the largest legal transcript tail. Tool-heavy requests therefore
retain less tail space than requests with a small base, while neither leaves
otherwise usable input stranded behind a fixed quarter-budget.

`ContextEstimator` is replaceable. `GenericContextEstimator` is deterministic
and deliberately conservative for non-ASCII text, tools, schemas, reminders,
and message framing. A provider-aware estimator can be supplied through
`agentcli.WithContextEstimator`.

## Summary and checkpoint creation

When the request exceeds the derived input budget, `Compactor` selects the
largest recent tail that begins at a legal conversation boundary and preserves
complete tool-call/result batches. Everything before that tail is serialized
in bounded chunks and merged cumulatively by a separate summarizer model. The
summarizer receives no tools and cannot recursively compact.

The prompt treats prior summaries and transcript history as untrusted data. It
requires a same-language Markdown result with anchored `Objective`, `Important
Details`, `Work State` (`Completed`, `Active`, `Blocked`), `Next Move`, and
`Relevant Files` sections. Exact file paths and IDs must be preserved.

After successful summarization, the runtime appends a
`compaction_checkpoint` containing the cumulative summary,
`CoversThroughMessageID`, and `TailStartMessageID`. Checkpoint boundaries must
be contiguous and monotonic across repeated compactions.

## Storage and provider projection

Message storage remains append-only: original messages are never deleted or
rewritten. Before a provider call, the runtime reads the latest valid
checkpoint and creates a temporary request containing:

1. a system message wrapping the cumulative summary; and
2. verbatim non-checkpoint messages beginning at `TailStartMessageID`.

Messages through `CoversThroughMessageID` remain in storage but are omitted
from that provider request. Checkpoint records themselves are also omitted.
Resumed sessions keep projecting their latest checkpoint even when automatic
creation is later disabled.

HTTP transcript responses identify checkpoint records by
`type: "compaction_checkpoint"` but intentionally omit summary and boundary
fields. Treat them as opaque internal runtime state.

## Events and child sessions

A new compaction emits `compaction_started` immediately before the summarizer,
persists the checkpoint, then emits `compaction_completed` before the main
provider starts. Preparation, summarizer, or persistence errors emit
`compaction_failed`, fail the run, and prevent that main-provider round.

No compaction event is emitted when the request already fits or when a resumed
session only projects an existing checkpoint. Project-created child agents
inherit the compaction model and estimator. Their events carry the child's own
session and turn IDs and do not appear as parent-run events; the parent
continues receiving the normal child callback lifecycle.

## Provider boundary

The main model must implement `ModelMetadataProvider` when compaction is
enabled. A summarizer that implements the optional capability is validated too.
Project-selected models use shared limits from the `compaction` mapping first,
then the provider `/models` endpoint, then `models.dev`, then exact project
defaults of 122,880 context tokens and 66,560 output tokens. Directly
constructed OpenAI-compatible adapters retain their exact-alias catalog.
Applications can still override project-selected adapters through
`agentcli.WithModel` and `agentcli.WithCompactionModel`.

Relevant implementation:

- `agentruntime/compactor.go`: estimation, selection, summarization, checkpoint
  validation, and projection.
- `agentruntime/run.go`: event ordering, checkpoint persistence, and
  main-provider admission.
- `agentruntime/context_estimator.go` and `model_metadata.go`: provider-neutral
  sizing contracts.
- `storage/message.go`: append-only checkpoint message domain.

Back to [architecture/index.md](index.md).
