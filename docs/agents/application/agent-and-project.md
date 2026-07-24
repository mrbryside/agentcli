# Agent and project configuration

`agentcli.New` uses functional `Option` values to assemble one runtime, one private tool executor, buffered transports, and in-memory message/permission/confirmation stores by default. The executor starts before `New` returns. `Agent.Close` cancels owned work and waits for lifecycle goroutines; always close a successfully constructed Agent.

The optional curl bootstrapper creates a runnable application around this
assembly API. Its prompts, placeholders, generated files, bounded starter
tools, and verification flow are documented in
[bootstrap-installer.md](../development/bootstrap-installer.md).

`LoadProject(root)` snapshots `.agentcli/config.yaml`, `.agentcli/MAIN.md`, root `AGENTS.md`, `.agentcli/skill/*/SKILL.md`, and `.agentcli/agent/*/*.md`. Provider map keys are arbitrary connection aliases; each profile requires a supported `type` (`openai` currently). Environment references are expanded, but `.env` is not loaded. `config.yaml` may set `max_subagents` to bound non-closed child instances per parent session; omitted values use the default of 4. `MAIN.md` selects a provider alias, model, optional skills/tools, and instructions. Startup validation rejects missing or unsupported provider types, negative quotas, unknown profiles or skills, and registered-tool allowlist mismatches.

Applications explicitly register executable capabilities through `WithTool`;
project Markdown only selects names from the registered catalog. The root
package exposes `Tool`, `ToolDefinition`, schema builders,
`DecodeArguments`, admission aliases, and turn behavior so ordinary
applications do not need runtime-package imports.

Back to [application/index.md](index.md).
