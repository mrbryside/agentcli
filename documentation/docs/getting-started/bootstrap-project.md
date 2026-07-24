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
available, it resolves `github.com/mrbryside/agentcli@latest` directly from Git
and then runs `go mod tidy`, so the generated project uses the newest published
semver tag without a lagging module proxy. Set `AGENTCLI_VERSION` to pin a
release or test an unreleased branch. If Go is not installed yet, the module
falls back to Go `1.26.3`, pins the current fallback release in `go.mod`, and
prints the commands to run later.

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
  response. The generated prompt forbids direct conversational, progress, or
  final messages to the user; user-facing content must be delivered only
  through the final call's `message` argument. The agent may set
  `skipReport: true` after deciding that the turn has no useful user-facing
  content worth reporting; omitting it or setting it to `false` records the
  message. The tool performs no network I/O and appends each reported payload to
  `report/{session}.json`, and is not available to the researcher. Its public
  result only reports completion; the session/turn/call metadata remains in
  the local log. A built-in prompt tool-call guard checks message bounds,
  disclosure policy, direct standalone reporting, and the `skipReport`
  decision before the handler runs. A reported message must present actions,
  status, findings, and conclusions as if the main agent performed the work
  itself. It must not mention delegation, another agent/subagent/researcher,
  waiting for one, or a promised later update. Internally delegated findings
  are reported directly without attribution. Rejection leaves the report file
  unchanged and becomes a failed tool result with feedback so the main agent
  can issue a corrected finalizer call.

The report decision has explicit positive skip semantics:

| `skipReport` | Result status | Report file |
| --- | --- | --- |
| omitted or `false` | `reported` | Appends `message` to `report/{session}.json`. |
| `true` | `skipped` | Does not create or append a report entry. |

`message` remains required in both cases. When skipping, it briefly states why
no report is necessary, but the handler does not record it. The old `report`
field is not accepted; strict argument decoding rejects it so an inverted
boolean cannot silently select the wrong behavior.

For example, `"A researcher is analyzing main.go; results will follow"` is
rejected. A direct result such as `"main.go loads the project, registers four
tools, and starts the terminal runtime"` is eligible for reporting.

Read and glob declare low-risk filesystem-read permission. Edit uses a bounded
atomic replacement after both gates succeed. The generated project
starts in `criticalOnly`, which allows low-risk requests unless an explicit
policy rule says otherwise. When a subagent permission or confirmation needs a
decision, the request is surfaced in the parent Terminal session; you do not
need to open the child view.

The generated tools use the public explicit schema API: `agentcli.Tool`,
`agentcli.ToolDefinition`, `agentcli.ToolParameter`, and
`agentcli.ObjectSchema`. Their raw handlers use `agentcli.DecodeArguments`,
and `main.go` registers each one with `agentcli.WithTool`, so generated code
does not import runtime implementation packages.

The `report_discord` prompt check uses the configured main model and adds one
model request for each requested call it evaluates before handler execution.
It is a demonstration policy, not a network or process sandbox. See
[Tool-call guards](../guardrails/tool-call.md) before replacing the mock
with an external integration.

## Run the project

After replacing the placeholders, export the provider key and run the app:

```sh
cd my-agent
export API_KEY='replace-with-a-real-key'
go run .
```

Continue with [Project configuration](project-configuration.md) for provider,
agent, skill, and tool allowlist details.
