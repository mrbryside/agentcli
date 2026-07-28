# Context compaction

Read this file when changing automatic context sizing, summary generation,
checkpoint storage, request projection, compaction events, or subagent-agent
inheritance.

## Preflight and budgets

Compaction runs immediately before every main-model round. The runtime builds a
provider-neutral `ModelRequest`, estimates its full input, and compares it with
the main model's validated context-window and output-limit metadata. Internal
input, recent-tail, serialized-recent-context, and summary budgets are derived
in code; only model limits are configurable in project YAML.

The model's output metadata remains a capability limit. Compaction applies an
operational output cap equal to one eighth of the main model's context window,
capped at 16,384 tokens and reduced further by a lower explicit request limit
or lower model capability. That same value is sent to the main provider and
reserved from the context budget, so sizing and generation cannot diverge.
Summary and estimator safety reserves each scale with the remaining input and
cap at 4,096 tokens. The checkpoint's serialized recent context scales with
the same input and caps at 8,192 tokens.
Recent history does not use a fixed percentage: the estimator first charges
system prompts, context reminders, tool schemas, the bounded summary
placeholder, and the safety reserve. The verbatim recent-tail target is then
one quarter of usable input, clamped between 2,048 and 8,192 tokens. This keeps
substantial room for tool results produced by subsequent rounds instead of
filling the request immediately after compaction. System and tool base costs
can reduce the tail further.

`ContextEstimator` is replaceable. By default the runtime asks the main model's
optional `ContextEstimatorProvider` capability, which makes root and subagent
agents select the estimator for their actual provider. `GenericContextEstimator`
is the deterministic conservative fallback. It charges ASCII at three bytes
per token to leave headroom for JSON, code, and tokenizers that are denser than
the common four-byte approximation, while charging every non-ASCII UTF-8 byte.
It also includes tools, schemas, reminders, and message framing.
`agentcli.WithContextEstimator` remains an explicit application-wide override.

If a model adapter identifies a provider rejection as
`ErrContextWindowExceeded`, the runtime force-compacts once and retries that
provider round once. Forced compaction is provider-neutral: it retains the
newest complete conversation unit, summarizes older legal units, and never
splits a tool-call/result batch. Adapters translate provider-specific errors
into the shared sentinel; the compactor contains no provider-name checks.

## Summary and checkpoint creation

When the request exceeds the derived input budget, `Compactor` selects the
largest recent tail that begins at a legal conversation boundary and preserves
complete tool-call/result batches. It serializes everything before that native
tail in a readable role-labelled form. Tool outputs are capped at 2,000
characters, while user and assistant content remains intact.

The newest bounded suffix of that serialization is stored directly as
`RecentContext`. The older serialization, the prior checkpoint's
`RecentContext`, and the prior summary are sent to a separate tool-free
summarizer in exactly one model call. There is no sequential chunk loop. If
the one-shot request cannot fit the summarizer's context window, compaction
returns `ErrCompactionPromptTooLarge` without starting the summarizer or
creating a checkpoint.

If one active turn contains so many completed tool rounds that the whole turn
cannot fit, selection retries inside that turn. The fallback summarizes its
older prefix and retains the largest recent suffix beginning with an assistant
message or a complete tool-call/result batch. Provider projection inserts a
synthetic runtime continuation immediately before that suffix, so the retained
assistant activity is never sent as an orphan. This fallback is only used when
the ordinary user/runtime boundary cannot fit.

An indivisible latest assistant/tool unit that exceeds the recent-tail target
is retained whole when it still fits the provider input budget. This exception
preserves tool-call/result adjacency and avoids failing merely because one
complete provider unit is larger than 8,192 tokens.

The prompt treats prior summaries and transcript history as untrusted data. It
requires a same-language Markdown result with anchored `Objective`, `Important
Details`, `Work State` (`Completed`, `Active`, `Blocked`), `Next Move`, and
`Relevant Files` sections. Exact file paths and IDs must be preserved when
still relevant. Stale, superseded, and unrelated completed details must be
removed.

After successful summarization, the runtime appends a
`compaction_checkpoint` containing the cumulative summary, serialized recent
context,
`CoversThroughMessageID`, and `TailStartMessageID`. Checkpoint boundaries must
be contiguous and monotonic across repeated compactions.

## Storage and provider projection

Message storage remains append-only: original messages are never deleted or
rewritten. Before a provider call, the runtime reads the latest valid
checkpoint and creates a temporary request containing:

1. an assistant message clearly labelled as historical context, containing
   the cumulative summary and serialized recent context;
2. when the tail begins inside an active turn, a synthetic runtime
   continuation message; and
3. verbatim non-checkpoint messages beginning at `TailStartMessageID`.

Messages through `CoversThroughMessageID` remain in storage but are omitted
from that provider request. Checkpoint records themselves are also omitted.
Resumed sessions keep projecting their latest checkpoint even when automatic
creation is later disabled. Current system prompts and newer verbatim messages
explicitly take precedence over the historical checkpoint, so compacted
conversation data cannot become a new system instruction.

HTTP transcript responses identify checkpoint records by
`type: "compaction_checkpoint"` but intentionally omit summary and boundary
fields. Treat them as opaque internal runtime state.

## Events and subagent sessions

A new compaction emits `compaction_started` immediately before the summarizer,
persists the checkpoint, then emits `compaction_completed` before the main
provider starts. Preparation, summarizer, or persistence errors emit
`compaction_failed`, fail the run, and prevent that main-provider round.
The same event sequence is emitted for a forced compaction retry after a
context-window rejection.

No compaction event is emitted when the request already fits or when a resumed
session only projects an existing checkpoint. Project-created subagents
inherit the compaction model and any explicit estimator override; otherwise
each subagent selects the estimator from its own main model. Their events carry
the subagent's own session and turn IDs and do not appear as main agent-run events;
the main agent continues receiving the normal subagent result lifecycle.

## Provider boundary

The main model must implement `ModelMetadataProvider` when compaction is
enabled. A summarizer that implements the optional capability is validated too.
Each provider profile may supply explicit limits in exact-name model entries.
Those limits apply only to a matching model; otherwise each distinct main,
subagent, and summarizer model resolves its limits from the provider `/models`
endpoint, then `models.dev`, then project defaults of 122,880 context tokens
and 66,560 output tokens. Directly
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
