# Agent and project configuration

`agentcli.New` uses functional `Option` values to assemble one runtime, one private tool executor, buffered transports, and in-memory message/permission/confirmation stores by default. The executor starts before `New` returns. `Agent.Close` cancels owned work and waits for lifecycle goroutines; always close a successfully constructed Agent.

`curl -fsSL https://raw.githubusercontent.com/mrbryside/agentcli/main/init/install.sh | sh` runs the optional project bootstrapper. It prompts through the terminal for the project folder name and Go module path, then creates a minimal terminal application plus `.agentcli` configuration, an example skill, and a researcher subagent. It also registers bounded `glob` and `read` tools for the main agent and researcher. `read` accepts 1-based `offset` and a maximum 2,000-line `limit`, returning `next_offset` when the result is truncated; `glob` defaults to 100 results and cannot exceed 500. Applications can register their own tools in Go code.

`LoadProject(root)` snapshots `.agentcli/config.yaml`, `.agentcli/MAIN.md`, root `AGENTS.md`, `.agentcli/skill/*/SKILL.md`, and `.agentcli/agent/*/*.md`. Provider map keys are arbitrary connection aliases; each profile requires a supported `type` (`openai` currently). Environment references are expanded, but `.env` is not loaded. `MAIN.md` selects a provider alias, model, optional skills/tools, and instructions. Startup validation rejects missing or unsupported provider types, unknown profiles or skills, and registered-tool allowlist mismatches.

Applications explicitly register executable tools through `WithCustomTool` or advanced `WithTool`; project Markdown only selects among registered capabilities. Keep public configuration functional and keep provider-specific types behind adapters.

Back to [application/index.md](index.md).
