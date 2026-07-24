# Agent and project configuration

`agentcli.New` uses functional `Option` values to assemble one runtime, one private tool executor, buffered transports, and in-memory message/permission/confirmation stores by default. The executor starts before `New` returns. `Agent.Close` cancels owned work and waits for lifecycle goroutines; always close a successfully constructed Agent.

The optional curl bootstrapper creates a runnable application around this
assembly API. Its prompts, placeholders, generated files, bounded starter
tools, and verification flow are documented in
[bootstrap-installer.md](../development/bootstrap-installer.md).

`LoadProject(root)` snapshots `.agentcli/config.yaml`, `.agentcli/MAIN.md`, root `AGENTS.md`, `.agentcli/skill/*/SKILL.md`, and `.agentcli/agent/*/*.md`. Provider map keys are arbitrary connection aliases; each profile requires a supported `type` (`openai` currently). Environment references are expanded, but `.env` is not loaded. `config.yaml` may set `max_subagents` to bound non-closed child instances per parent session; omitted values use the default of 4. `MAIN.md` selects a provider alias, model, optional skills/tools, and instructions. Startup validation rejects missing or unsupported provider types, negative quotas, unknown profiles or skills, and registered-tool allowlist mismatches.

Provider profiles may declare reusable model limits under
`models.<model-id>.context_window_tokens` and `max_output_tokens`. When
compaction requires a model and no explicit entry exists, project
loading checks the provider's authenticated `/models` endpoint first because
it represents the active deployment. It falls back to `models.dev` only when
the provider supplies no valid limits. If neither source supplies unambiguous
limits, loading fails with a config snippet instead of guessing.

Applications explicitly register executable capabilities through `WithTool`;
project Markdown only selects names from the registered catalog. The root
package exposes `Tool`, `ToolDefinition`, schema builders,
`DecodeArguments`, admission aliases, and turn behavior so ordinary
applications do not need runtime-package imports.

## Automatic transcript compaction

`.agentcli/config.yaml` may contain an optional, strict `compaction` mapping
with exactly `auto`, `provider`, and `model`. Omitting the mapping disables new
compactions. When the mapping is present, `auto` defaults to `true`; set
`auto: false` to disable new compactions while retaining the configuration.
`provider` is an existing provider-profile alias, not a provider type, and
`model` selects the separate summarizer. It is constructed through the same
project model factory used by main and child agents.

Compaction has no YAML token-budget knobs. Provider-scoped `models` entries
describe model capabilities reusable by main, child, guard, and compaction
adapters; the runtime derives its internal input, recent-history, and summary
budgets from that metadata.
When it is enabled, construction requires known valid limits for the main
model. If the summarizer exposes `ModelMetadataProvider`, its limits are also
validated at startup; a summarizer without that optional capability uses the
internal summary cap. An unknown or invalid required catalog entry fails
startup rather than guessing.

The setting is inherited when the project creates child agents. It controls
only creation of future checkpoints: a resumed session always projects its
latest stored checkpoint, including after `auto` is later disabled.

`Agent.SendMessage(ctx, sessionID, message)` is the ordinary direct-client
entry point. It builds a user request, lets the runtime generate turn/message
identity and timestamps, and returns a subscription installed before
`RunStarted`. Reusing the session ID continues its stored transcript. Advanced
callers use `Start` or `StartSubscribed` when they need an explicit turn ID or
trusted runtime-event input. Root aliases expose runs, subscriptions, agent
events, provider event constants, statuses, and common runtime errors without
requiring application imports from `agentruntime` or `provider`.

Back to [application/index.md](index.md).
