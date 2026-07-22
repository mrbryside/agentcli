# Agent documentation

This folder holds context documents for agents. Treat every `index.md` as a jump point—only open the linked files relevant to the current task.

| If you want to know... | Go to |
| --- | --- |
| Root index | [../../AGENTS.md](../../AGENTS.md) |
| Runtime architecture and event semantics | [architecture/index.md](architecture/index.md) |
| High-level `agentcli` integration surfaces | [application/index.md](application/index.md) |
| Tool execution and safety gates | [tools-safety/index.md](tools-safety/index.md) |
| Storage and provider boundaries | [boundaries/index.md](boundaries/index.md) |
| Repository development and verification | [development/index.md](development/index.md) |

## Maintenance note

When adding new context:

1. Put detail in the relevant `docs/agents/{category}/` subfile.
2. If the category does not exist, create the folder and an `index.md`.
3. Link the new subfile from the category `index.md`.
4. If it is a new top-level category, add a row to this table and the root table.
5. Never paste long details directly into `AGENTS.md`.
6. Any new document under `docs/agents/` must follow the same index style as `AGENTS.md`: start with a short "when to read this" description, use an "If you want to know X → go to file Y" table when it covers multiple subtopics, and keep long details in linked subfiles.
