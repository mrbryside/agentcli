---
title: Bootstrap a project
sidebar_position: 2
---

# Bootstrap a project

Create a runnable terminal-agent project with one command:

```sh
curl -fsSL https://raw.githubusercontent.com/mrbryside/agentcli/main/init/install.sh | sh
```

The installer reads from your terminal and asks for:

```text
Project folder name (for example my-agent):
Go module path (for example github.com/you/my-agent):
```

No `sh -s --` argument is required. The selected project folder must not
already exist; the installer refuses to overwrite it.

## Generated project

The starter contains:

```text
my-agent/
├── go.mod
├── main.go
├── tool_edit.go
├── tool_glob.go
├── tool_read.go
├── tool_report_discord.go
└── .agentcli/
    ├── config.yaml
    ├── MAIN.md
    ├── skill/
    │   └── interview/SKILL.md
    └── agent/
        └── researcher/researcher.md
```

The installer never asks for, writes, or loads provider credentials.
`${API_KEY}` in the generated configuration is resolved only from the
process environment.

The installer detects the local `go env GOVERSION` for `go.mod`. When Go is
available, it resolves `github.com/mrbryside/agentcli@main` directly from Git
and then runs `go mod tidy`, avoiding a lagging module proxy so the generated
templates and library API match the same main branch. If Go is not installed yet, the module falls back to Go
`1.26.3` and prints the commands to run later.

## Replace provider and model placeholders

Generated agent definitions intentionally use conspicuous placeholders:

```yaml
provider: replace-provider
model: replace-model
```

`.agentcli/config.yaml` defines the matching provider alias:

```yaml
permission_mode: criticalOnly

providers:
  replace-provider:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
    request_timeout: 2m
```

Replace `replace-provider` consistently in `config.yaml`, `MAIN.md`, and every
file under `.agentcli/agent/`. Replace `replace-model` in `MAIN.md` and every
subagent definition with a model supported by that provider. The provider
alias is application-defined; `type: openai` selects the OpenAI-compatible
adapter and does not require the alias itself to be `openai`.

For example:

```yaml
# .agentcli/config.yaml
providers:
  primary:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
```

```yaml
# .agentcli/MAIN.md and researcher.md frontmatter
provider: primary
model: your-model-name
```

## Starter tools

The main agent selects `glob`, `read`, `edit`, and `report_discord`; the sample
researcher selects only `glob` and `read`:

- `glob` searches only below the project root, supports recursive `**`,
  excludes sensitive paths, defaults to 100 matches, and returns at most 500.
- `read` returns UTF-8 text only, excludes sensitive paths, reads at most 2,000
  lines and 256 KiB per call, and returns `next_offset` when more lines remain.
- `edit` replaces exactly one occurrence of `old_string` with `new_string` in
  an existing UTF-8 file. It rejects missing or ambiguous matches, symlinks,
  sensitive paths, and writes outside the project. Each call requires high-risk
  `filesystem.write` permission and a separate confirmation; the researcher is
  not allowed to use it.
- `report_discord` is a deterministic mock finalizer. The main agent calls it
  exactly once as the standalone final action with the complete user-facing
  response; it performs no network I/O and is not available to the researcher.

Read and glob declare low-risk filesystem-read permission. Edit uses a bounded
atomic replacement after both gates succeed. The generated project
starts in `criticalOnly`, which allows low-risk requests unless an explicit
policy rule says otherwise. When a subagent permission or confirmation needs a
decision, the request is surfaced in the parent Terminal session; you do not
need to open the child view.

The generated tools use the public typed schema API: `agentcli.Tool`,
`agentcli.ToolParameter`, and `agentcli.ObjectSchema`. Their handlers still
receive raw JSON, so custom decoding remains straightforward without importing
runtime implementation packages.

## Run the project

After replacing the placeholders, export the provider key and run the app:

```sh
cd my-agent
export API_KEY='replace-with-a-real-key'
go run .
```

Continue with [Project configuration](project-configuration.md) for provider,
agent, skill, and tool allowlist details.
