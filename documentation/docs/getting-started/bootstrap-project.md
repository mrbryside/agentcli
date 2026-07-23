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
OpenAI API key (leave blank to configure .env later):
```

No `sh -s --` argument is required. The selected project folder must not
already exist; the installer refuses to overwrite it.

## Generated project

The starter contains:

```text
my-agent/
├── .env                         # only when a key was supplied
├── .gitignore
├── go.mod
├── main.go
├── tool_glob.go
├── tool_read.go
└── .agentcli/
    ├── config.yaml
    ├── MAIN.md
    ├── skill/
    │   └── interview/SKILL.md
    └── agent/
        └── researcher/researcher.md
```

The generated application loads `OPENAI_API_KEY` from `.env` when the process
environment does not already define it. `.env` is ignored by Git, and a key
entered during installation is written with restrictive file permissions.

The installer detects the local `go env GOVERSION` for `go.mod` and runs
`go mod tidy` when Go is available. If Go is not installed yet, the module
falls back to Go `1.26.3` and prints the commands to run later.

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
    api_key: ${OPENAI_API_KEY}
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
    api_key: ${OPENAI_API_KEY}
```

```yaml
# .agentcli/MAIN.md and researcher.md frontmatter
provider: primary
model: your-model-name
```

## Starter tools

The main agent and sample researcher both select `glob` and `read`:

- `glob` searches only below the project root, supports recursive `**`,
  excludes sensitive paths, defaults to 100 matches, and returns at most 500.
- `read` returns UTF-8 text only, excludes sensitive paths, reads at most 2,000
  lines and 256 KiB per call, and returns `next_offset` when more lines remain.

Both tools declare low-risk filesystem-read permission. The generated project
starts in `criticalOnly`, which allows low-risk requests unless an explicit
policy rule says otherwise. When a subagent permission or confirmation needs a
decision, the request is surfaced in the parent Terminal session; you do not
need to open the child view.

## Run the project

After replacing the placeholders:

```sh
cd my-agent
go run .
```

If the API key was left blank during installation, create `.env` first:

```sh
printf 'OPENAI_API_KEY=%s\n' 'replace-with-a-real-key' > .env
chmod 600 .env
```

Continue with [Project configuration](project-configuration.md) for provider,
agent, skill, and tool allowlist details.
