# Agent and project configuration

`agentcli.New` uses functional `Option` values to assemble one runtime, one private tool executor, buffered transports, and in-memory message/permission/confirmation stores by default. The executor starts before `New` returns. `Agent.Close` cancels owned work and waits for lifecycle goroutines; always close a successfully constructed Agent.

The optional curl bootstrapper creates a runnable application around this
assembly API. Its prompts, placeholders, generated files, bounded starter
tools, and verification flow are documented in
[bootstrap-installer.md](../development/bootstrap-installer.md).

`LoadProject(root)` snapshots `.agentcli/config.yaml`, `.agentcli/MAIN.md`,
`.agentcli/skill/*/SKILL.md`, and `.agentcli/agent/*/*.md`. Provider map keys
are arbitrary connection aliases; each profile requires a supported `type`
(`openai` currently). Environment references are expanded, but `.env` is not
loaded. `config.yaml` may set `max_subagents` to bound non-closed subagent
instances per main-agent session (default 4).

Provider-step limiting is opt-in through `WithProviderStepLimit(n)` and is
inherited by subagents; omission leaves turns unlimited. Exhaustion enters a
restricted finalization phase that removes ordinary work tools. Main-agent
finalization retains its registered `EndTurn` and `EndResponseScope` tools;
subagent finalization retains the framework-owned `report_subagent_result`.
With no required completion tool, the finalizer returns a text summary from
existing results and cannot resume ordinary work. `MAIN.md` selects a provider
alias, model, optional skills/tools, and instructions. Startup validation
rejects missing or unsupported provider types, negative limits, unknown
profiles or skills, registered-tool allowlist mismatches, and any subagent
assignment of an `EndResponseScope` tool.

Provider requests keep the framework prompt and the `MAIN.md` body (or subagent
definition body) in separate ordered system messages. Main-agent projects
insert a framework-owned skill-discovery system message when skills are configured, then
a subagent system message when subagents are configured, before `MAIN.md`. The
subagent message owns the focused tool-result protocol, the complete
orchestration rules, and the available-subagent catalog. Capability
instructions are therefore contiguous and not duplicated in the general
framework prompt. Root `AGENTS.md` is not loaded. Prompt material is rebuilt
from the project snapshot rather than persisted in conversation history.

Provider profiles may contain optional exact-name entries under `models`.
These entries are overrides, not an allowlist: `MAIN.md` and subagents may
select unlisted models normally. A matching entry may set the tri-state
`reasoning` boolean, which becomes the Qwen-compatible
`chat_template_kwargs.enable_thinking` value, and an arbitrary `extra_body`
mapping. `extra_body` is recursively environment-expanded and merged as
top-level JSON only for requests using that exact model name.

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
project model factory used by main and subagents.

Optional `context_window_tokens` and `max_output_tokens` belong to each exact
model entry. They take priority only for that model, so main, subagent, and
summarizer adapters can use independent limits while sharing a profile. When
omitted, project loading checks each required model's authenticated provider `/models`
endpoint first, falls back to `models.dev`, then uses exact defaults of 122,880
context tokens and 66,560 output tokens. The Terminal displays those binary
counts as `120k` and `65k`.
When it is enabled, construction requires known valid limits for the main
model. If the summarizer exposes `ModelMetadataProvider`, its limits are also
validated at startup; a summarizer without that optional capability uses the
internal summary cap. Explicit non-positive or partially configured model
limits fail startup validation.

The setting is inherited when the project creates subagents. It controls
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
