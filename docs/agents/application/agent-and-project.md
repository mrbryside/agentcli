# Agent and project configuration

`agentcli.New` uses functional `Option` values to assemble one runtime, one private tool executor, buffered transports, and in-memory message/permission/confirmation stores by default. The executor starts before `New` returns. `Agent.Close` cancels owned work and waits for lifecycle goroutines; always close a successfully constructed Agent.

`LoadProject(root)` snapshots `.agentcli/config.yaml`, `.agentcli/MAIN.md`, root `AGENTS.md`, `.agentcli/skill/*/SKILL.md`, and `.agentcli/agent/*/*.md`. Provider map keys are arbitrary connection aliases; each profile requires a supported `type` (`openai` currently). Environment references are expanded, but `.env` is not loaded. `MAIN.md` selects a provider alias, model, optional skills/tools, and instructions. Startup validation rejects missing or unsupported provider types, unknown profiles or skills, and registered-tool allowlist mismatches.

Applications explicitly register executable tools through `WithCustomTool` or advanced `WithTool`; project Markdown only selects among registered capabilities. Keep public configuration functional and keep provider-specific types behind adapters.

Back to [application/index.md](index.md).
