# AGENTS.md — agentcli

Go library for provider-neutral, event-sourced agent runs with tool execution, safety gates, Terminal UI, and HTTP integration surfaces.

`Last documented commit: 03eb44d189603dd58f209d01c030878fde4ae67d`

## Project structure

| Path | Purpose |
| --- | --- |
| `.agentcli/` | Example project definitions: `MAIN.md`, provider config template, skills, and subagents. |
| `.github/workflows/` | GitHub Actions automation, including Docusaurus deployment to GitHub Pages. |
| `init/` | Curl bootstrap installer and separately downloadable `read`/`glob` Go tool templates for generated starter projects. |
| Root `*.go` files | Public `agentcli` package: Agent assembly, project loading, custom tools, subagents, Terminal UI, and Echo HTTP/SSE server. |
| `Makefile` | Convenience entry points for the terminal playground and documentation install/build/dev workflows. |
| `agentruntime/` | Session/turn coordination, retained agent events, live subscriptions, interruption, and state/effect/result folding. |
| `agentruntime/modeladapter/openai/` | Provider-boundary conversion from generic messages and tools to OpenAI chat requests. |
| `provider/` | Provider-neutral streaming interfaces, events, state, subscriptions, and results. |
| `provider/openai/` | OpenAI-compatible streaming provider and chunk parser. |
| `storage/` | Provider-neutral message, permission, confirmation, and subagent storage contracts. |
| `storage/inmemory/` | Concurrency-safe in-memory implementations used by default. |
| `toolexecution/` | Tool registry, framework tools, permission/confirmation admission, interrupts, and bounded workers. |
| `toolexecution/bashsecure/` | Optional shell command parsing, path scoping, policy classification, and platform sandbox helpers. |
| `permission/` | Capability, risk, mode, policy, request, decision, and grant domain. |
| `confirmation/` | Independent invocation-specific Yes/No confirmation domain. |
| `playground/terminal/` | Runnable Terminal UI playground with caller-owned `glob`, `read`, and `confirm_demo` tools. |
| `documentation/` | Docusaurus guides plus generated Swaggo/OpenAPI and Redocly API reference. |
| `docs/agents/` | Context-saving agent documentation indexes and focused subtopics. |

Only open the sections below when they are relevant to the current task.

| If you want to know... | Go to |
| --- | --- |
| Runtime architecture, session/turn ownership, or event history semantics | [docs/agents/architecture/index.md](docs/agents/architecture/index.md) |
| Agent construction, project loading, terminal/server surfaces, skills, or subagents | [docs/agents/application/index.md](docs/agents/application/index.md) |
| Tool registration/execution, permissions, confirmations, or shell safety | [docs/agents/tools-safety/index.md](docs/agents/tools-safety/index.md) |
| Generic storage domains, in-memory behavior, providers, or OpenAI conversion | [docs/agents/boundaries/index.md](docs/agents/boundaries/index.md) |
| Testing, documentation generation, examples, or the Terminal UI playground | [docs/agents/development/index.md](docs/agents/development/index.md) |

## Maintenance note

When adding new context:

1. Put detail in the relevant `docs/agents/{category}/` subfile.
2. If the category does not exist, create the folder and an `index.md`.
3. Link the new subfile from the category `index.md`.
4. If it is a new top-level category, add a row to the table above.
5. Never paste long details directly into `AGENTS.md`.
6. Any new document under `docs/agents/` must follow the same index style as `AGENTS.md`: start with a short "when to read this" description, use an "If you want to know X → go to file Y" table when it covers multiple subtopics, and keep long details in linked subfiles.
