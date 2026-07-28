# Provider-neutral auto-compaction implementation plan

Implement automatic transcript compaction before provider rounds without coupling
the runtime to OpenAI request types, tokenizers, or model names.

## Resolved design

- `.agentcli/config.yaml` enables compaction with only `auto`, `provider`, and
  `model`. The section is optional; when present, `auto` defaults to true.
- The compaction provider is an existing provider-profile alias and the model is
  resolved through the same project model factory as main agents and subagents.
- Context sizing is a provider-neutral model capability. Runtime code sees only
  generic requests and model metadata; adapters own provider/model-specific
  metadata resolution.
- Internal budgets are derived from context and output limits. They are not
  exposed in YAML in the first version.
- Transcript storage remains append-only. Compaction creates a cumulative
  checkpoint and provider requests project the latest checkpoint plus a recent
  verbatim tail; original messages remain available.
- The summarizer receives a serialized, bounded history and an anchored,
  structured prompt. It receives no tools and its own call cannot recursively
  compact.

## Tasks

- [x] **Task 1 — Storage checkpoint domain**
  - Depends on: none
  - Add the provider-neutral compaction checkpoint message shape, validation,
    cloning, and in-memory storage coverage.
  - Preserve existing message invariants and full transcript ordering.
  - Verify: `go test ./storage/...`

- [x] **Task 2 — Model context capability and estimator**
  - Depends on: none
  - Add generic context-window/output-limit metadata and a replaceable
    `ContextEstimator`.
  - Provide a deterministic generic estimator over system prompts, reminders,
    messages, and tool definitions.
  - Keep OpenAI types out of the runtime package.
  - Verify: `go test ./agentruntime/...`

- [x] **Task 3 — Strict project config and compaction model resolution**
  - Depends on: none
  - Add the optional `compaction` YAML section with strict validation.
  - Resolve its provider/model through project provider profiles.
  - Apply it through `WithProject`, and inherit it when creating subagents.
  - Update the example config and project-loading tests.
  - Verify: `go test .`

- [x] **Task 4 — Core compactor**
  - Depends on: Tasks 1 and 2
  - Implement recent-tail selection on legal conversation boundaries, bounded
    serialization, anchored prompt generation, previous-summary merging, and a
    tool-free summarizer stream call.
  - Derive reserve/recent/summary budgets from model capabilities.
  - Verify focused compactor unit tests.

- [x] **Task 5 — Runtime preflight integration and events**
  - Depends on: Tasks 1, 2, and 4
  - Run compaction preflight immediately before the main provider starts.
  - Persist a checkpoint, rebuild effective context, re-estimate, and fail
    clearly if the request still cannot fit.
  - Add observable compaction started/completed/failed events without exposing
    summarizer text as ordinary assistant output.
  - Verify runtime integration, resume, repeated-compaction, interruption, and
    concurrent-session tests.

- [x] **Task 6 — OpenAI adapter metadata support**
  - Depends on: Task 2
  - Implement the generic model-context capability at the OpenAI-compatible
    boundary without leaking SDK types into runtime.
  - Validate known-model metadata behavior and explicit failure for unresolved
    limits when compaction is enabled.
  - Verify adapter/provider tests.

- [x] **Task 7 — Documentation and examples**
  - Depends on: Tasks 3, 5, and 6
  - Document YAML behavior, defaults, provider-neutral extension points,
    lifecycle events, resume semantics, and failure modes.
  - Update context-saving agent docs and public getting-started examples.
  - Verify documentation build if public docs changed.

- [x] **Task 8 — Integration audit**
  - Depends on: Tasks 1–7
  - Review all changes for provider leakage, message/tool adjacency, event
    cloning, subagent inheritance, and backward compatibility.
  - Verify: `go test ./...`, `go test -race ./...`, and `go vet ./...`.
