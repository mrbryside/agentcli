# Agent and project configuration

`agentcli.New` uses functional `Option` values to assemble one runtime, one private tool executor, buffered transports, and in-memory message/permission/confirmation stores by default. The executor starts before `New` returns. `Agent.Close` cancels owned work and waits for lifecycle goroutines; always close a successfully constructed Agent.

The optional curl bootstrapper creates a runnable application around this
assembly API. Its prompts, placeholders, generated files, bounded starter
tools, and verification flow are documented in
[bootstrap-installer.md](../development/bootstrap-installer.md).

`LoadProject(root)` snapshots `.agentcli/config.yaml`, `.agentcli/MAIN.md`, root `AGENTS.md`, `.agentcli/skill/*/SKILL.md`, and `.agentcli/agent/*/*.md`. Provider map keys are arbitrary connection aliases; each profile requires a supported `type` (`openai` currently). Environment references are expanded, but `.env` is not loaded. `config.yaml` may set `max_subagents` to bound non-closed child instances per parent session; omitted values use the default of 4. `MAIN.md` selects a provider alias, model, optional skills/tools, and instructions. Startup validation rejects missing or unsupported provider types, negative quotas, unknown profiles or skills, and registered-tool allowlist mismatches.

Provider profiles may set the optional tri-state `reasoning` boolean. Omission
preserves the backend default. For Qwen-compatible OpenAI chat endpoints,
`false` and `true` become the matching `chat_template_kwargs.enable_thinking`
value on every request using that profile.

Applications explicitly register executable capabilities through `WithTool`;
project Markdown only selects names from the registered catalog. The root
package exposes `Tool`, `ToolDefinition`, schema builders,
`DecodeArguments`, admission aliases, and turn behavior so ordinary
applications do not need runtime-package imports.

## Automatic transcript compaction

`.agentcli/config.yaml` may contain an optional, strict `compaction` mapping
with `auto`, `provider`, and `model`. Omitting the mapping disables new
compactions. When the mapping is present, `auto` defaults to `true`; set
`auto: false` to disable new compactions while retaining the configuration.
`provider` is an existing provider-profile alias, not a provider type, and
`model` selects the separate summarizer. It is constructed through the same
project model factory used by main and child agents.

Optional `context_window_tokens` and `max_output_tokens` belong to each
provider profile. They take priority for every model using that profile, so
main, child, and summarizer adapters can use independent limits. When omitted,
project loading checks each required model's authenticated provider `/models`
endpoint first, falls back to `models.dev`, then uses exact defaults of 122,880
context tokens and 66,560 output tokens. The Terminal displays those binary
counts as `120k` and `65k`.
When it is enabled, construction requires known valid limits for the main
model. If the summarizer exposes `ModelMetadataProvider`, its limits are also
validated at startup; a summarizer without that optional capability uses the
internal summary cap. Explicit non-positive or partially configured shared
limits fail startup validation.

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
