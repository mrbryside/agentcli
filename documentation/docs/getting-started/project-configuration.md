---
title: Project configuration
sidebar_position: 2
---

# Project configuration

Projects created by the curl bootstrapper begin with `replace-provider` and
`replace-model` placeholders. Replace the provider alias consistently in
the config's compaction/provider mappings, `MAIN.md`, and every subagent
definition, then replace each main, summarizer, and child model value before
running the project. The generated `report_discord` tool separately selects
the `guardrails` provider profile and `replace-guard-model`. See
[Bootstrap a project](bootstrap-project.md) for the generated layout.

`agentcli.LoadProject(root)` takes an immutable snapshot of project-owned
inputs:

```text
.agentcli/
├── config.yaml
├── MAIN.md
├── skill/
│   └── interview/SKILL.md
└── agent/
    └── researcher/researcher.md
```

`config.yaml` and `MAIN.md` are required. Skill and subagent directories are
optional. Invalid YAML, unknown frontmatter fields, missing provider profiles, unknown
skills, or unregistered tool allowlist entries cause initialization to fail.
This makes configuration mistakes visible before the first model request.

## Provider configuration

`.agentcli/config.yaml` owns connections, the initial permission mode, the
optional per-parent open-subagent quota, runtime logging, LLM observability,
and optional transcript compaction:

```yaml
permission_mode: default
max_subagents: 4

# Omit this mapping to disable runtime console logs.
logging:
  enabled: true
  level: info

# Omit this mapping to disable new compactions. When present, auto defaults to true.
compaction:
  auto: true
  provider: primary
  model: gpt-4.1-mini

providers:
  primary:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
    request_timeout: 2m
    models:
      gpt-4.1-mini:
        context_window_tokens: 122880
        max_output_tokens: 66560
        # reasoning: false # Optional Qwen-compatible shorthand.
        # extra_body: # Optional model-specific top-level request JSON.
        #   thinking:
        #     type: disabled

  openrouter:
    type: openai
    url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    request_timeout: 90s
```

Provider names are application-defined aliases. `MAIN.md` and subagent files
refer to the alias, while the required `type` field selects the adapter. Both
`primary` and `openrouter` above use `type: openai`, so their names can change
without changing protocol behavior. `openai` is currently the only supported
type; missing or unsupported types fail during `LoadProject`.

The optional `models` mapping holds overrides keyed by the exact model name
used in `MAIN.md`, a subagent definition, or `compaction.model`. It is not an
allowlist: agents may select unlisted model names, which continue through
metadata discovery/defaults without request overrides.

Within a matching model entry, the optional `reasoning` boolean is a
Qwen-compatible shorthand for `chat_template_kwargs.enable_thinking`. Omit it
to preserve the backend default. Provider extensions that use another wire
shape belong in the optional `extra_body` mapping. Its arbitrary YAML values
are encoded as JSON and merged into the top level of chat-completions requests
for that exact model. For example, a DeepSeek-compatible gateway can disable
thinking with `extra_body: {thinking: {type: disabled}}`, while a gateway using
a flat effort field can set `extra_body: {reasoning_effort: none}`. Environment
references are expanded recursively inside `extra_body`. Extra-body values
override standard request fields with the same names.

For example, one OpenAI-compatible gateway can configure DeepSeek and Qwen
independently while leaving every other model unmodified:

```yaml
providers:
  openzen:
    type: openai
    url: https://gateway.example/v1
    api_key: ${OPENZEN_API_KEY}
    models:
      deepseek-model-id:
        context_window_tokens: 122880
        max_output_tokens: 66560
        extra_body:
          thinking:
            type: disabled

      qwen-model-id:
        reasoning: false
```

If `MAIN.md` or a subagent selects `deepseek-model-id`, only the DeepSeek
entry is added to its requests. Selecting `qwen-model-id` sends only
`chat_template_kwargs.enable_thinking: false`. Selecting any other model name
still works and receives neither override.

Provider-step limits are programmatic rather than project configuration.
Without `WithProviderStepLimit`, main and child turns have no provider-round
ceiling. `WithProviderStepLimit(n)` allows `n` agentic provider rounds, then
makes exactly one additional request with no tools so the model can return a
final text summary; that finalizer is included in `RunResult.Steps`. It cannot
dispatch tools and does not run completion or output-guard repairs. The option
requires a positive value and is inherited by child agents.

`max_subagents` limits non-closed child instances per parent session. A positive
value sets the quota; omitting it or setting it to `0` keeps the default of 4.
Negative values are rejected. The Go option `WithMaxSubagents` can override the
project value when constructing an Agent.

Environment substitutions use `${NAME}`. A missing variable is a load error;
the loader does not silently send an empty credential.

## Runtime logging

The optional `logging` mapping emits structured runtime lifecycle records to
stderr. Omitting it or setting `enabled: false` disables the records. When the
mapping is present, `enabled` defaults to `true` and `level` defaults to
`info`; supported levels are `debug`, `info`, `warn`, and `error`.

Info logging covers turn and response-scope start/end, repair requests, and
terminal failures. Repair records identify output-guard versus
completion-guard retries, their attempt number, provider-step count, and any
restricted tool allowlist. Debug logging additionally includes provider
content, tool arguments/results, and compaction details. It also records callback-obligation
cancellation when an application-owned child close releases a response-scope
barrier. Delivery failures are error records. Tool JSON fields that look like tokens,
secrets, passwords, authorization values, or API keys are redacted and large
values are truncated. Model reasoning, guard feedback, and completion
reminders are never logged. Rejected repair drafts remain in retained run
events for diagnostics but never enter conversation storage or the next model
request.

Programmatic agents can use `WithLogger` to supply their own `*slog.Logger`.
When applied after `WithProject`, it overrides project logging. Child agents
reuse the selected root logger automatically.

See [Runtime logging](../observability/runtime-logging.md) for the event and
privacy reference.

## Langfuse LLM observability

The optional `observability.langfuse` mapping exports one OpenTelemetry
generation span for every model call. It instruments the provider-neutral
model boundary, so main-agent, subagent, prompt-guard, tool-call guard, and
compaction model calls use the same exporter. Tool handler execution and other
runtime events are not traced.

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

`base_url` defaults to `https://cloud.langfuse.com`. Use the matching Langfuse
Cloud region or the root URL of a self-hosted installation. Agentcli appends
`/api/public/otel/v1/traces`, authenticates with the configured project keys,
and requests Langfuse ingestion version 4. `sample_rate` defaults to `1.0` and
accepts values from `0` through `1`.

Every generation carries the runtime `SessionID` as `langfuse.session.id`, so
multiple turns and provider rounds appear in the same Langfuse session.
`TurnID`, provider profile, model, finish reason, release, environment,
latency, first-output time, and errors are attached automatically; they do not
belong in YAML.

Input, output, and reasoning capture all default to `false`. Enable only the
payloads that the project's data policy permits. Input includes system
prompts, messages, context reminders, and tool schemas. Reasoning is controlled
separately from normal output and remains omitted in the example above.

The root Agent owns one asynchronous exporter shared by its child agents.
Always call `Agent.Close()` during graceful shutdown so queued observations are
flushed. Export failures do not change model-call results.

## Automatic transcript compaction

The optional `compaction` mapping accepts `auto`, `provider`, and `model`;
unknown keys are rejected. Omitting it disables new compactions. If the mapping
is present, `auto` defaults to `true`; use `auto: false` to keep the mapping but
disable creation of future checkpoints.

`provider` must be an existing provider-profile alias such as `primary`, and
`model` is the separate summarizer model. It is resolved through the same
factory as the main agent and subagents; the alias's `type` still chooses the
adapter. Optional `context_window_tokens` and `max_output_tokens` live on each
exact model entry. This lets main, child, and summarizer models carry
independent limits even when they share a provider profile. The runtime derives
compaction budgets from the active main model's metadata.

With compaction enabled, the main model must expose valid context-window and
output metadata. Explicit limits on its exact model entry take priority and
avoid metadata requests for that model. Without them, each distinct
model requests its authenticated provider `/models` endpoint first, then
`https://models.dev/api.json`; the final defaults are 122,880 context tokens
and 66,560 output tokens. The Terminal UI displays those binary token counts
as `120k` and `65k`. Explicit non-positive or partially configured limits
remain validation errors.
Applications can still override project-selected adapters with
`agentcli.WithModel` or `agentcli.WithCompactionModel`. The optional
`ContextEstimatorProvider` capability selects a provider-aware estimator per
main model. `agentcli.WithContextEstimator` remains an explicit override when
the default selection is not precise enough.

Compaction preserves full transcript storage. When a request needs shrinking,
the runtime appends a cumulative checkpoint and sends the main model that
summary plus a recent verbatim tail. Resuming the session continues projecting
the latest stored checkpoint even if `auto` is later disabled; disabling it
only prevents new checkpoints. See
[Context compaction](../capabilities/context-compaction.md) for request
projection, lifecycle events, subagent behavior, and provider-neutral sizing.

## Main-agent definition

`.agentcli/MAIN.md` is always loaded, so it does not need `name` or
`description`:

```markdown
---
provider: primary
model: gpt-4.1-mini
skills:
  - interview
tools:
  - lookup_topic
  - publish_report
---

Understand the requested outcome, use capabilities deliberately, and provide a
clear self-contained result.
```

`skills` and `tools` are strict allowlists. Omit a key when the main agent gets
none of that capability. An explicit empty list is rejected to avoid confusing
"configured empty" state.

Listing a custom tool does not create its handler. The Go application must also
register that exact name with `agentcli.WithTool`; otherwise
`agentcli.New` returns an error such as:

```text
root agent requires custom tool "publish_report", but it is not registered
```

Registration makes a handler available to the application catalog; each agent
allowlist determines whether that model can see it. A required end-of-turn tool
is required only for agents whose allowlist exposes it. The generated
researcher intentionally exposes `glob` and `read`, not `edit` or
`report_discord`.

## Project instructions

Project loading creates exactly two system messages:

1. One framework message containing runtime rules, environment/model context,
   and discovery-only skill/subagent catalogs.
2. One main-agent instruction message containing the body of `MAIN.md`.

Root `AGENTS.md` is not loaded. Neither system message is persisted in
conversation storage; both are rebuilt for provider calls from the loaded
project snapshot. Subagents use the same separation, with their definition
body as the second system message.

## Programmatic overrides

`WithProject` applies the loaded model, prompts, root identity, permission mode,
skills, and subagents. Later scalar options can override it:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithPermissionMode(permission.CriticalOnly),
    agentcli.WithToolWorkers(8),
)
```

Use `WithProjectRoot` only when constructing without a loaded project. It sets
the identity used for project-scoped permission grants; it does not sandbox or
register tools.

## Configuration checklist

- Keep API keys in the process environment.
- Keep Langfuse payload capture aligned with the project's data policy.
- Give every main/subagent tool an exact registered-name match.
- Keep tool allowlists minimal.
- Describe skills and subagents narrowly enough that the model can avoid
  unnecessary activation.
- Start in `default` while testing safety classifications.
