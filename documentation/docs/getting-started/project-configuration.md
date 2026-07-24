---
title: Project configuration
sidebar_position: 2
---

# Project configuration

Projects created by the curl bootstrapper begin with `replace-provider` and
`replace-model` placeholders. Replace the provider alias consistently in
`config.yaml`, `MAIN.md`, and every subagent definition, then replace each
model value before running the project. The generated `report_discord` tool
separately selects the `guardrails` provider profile and
`replace-guard-model`. See
[Bootstrap a project](bootstrap-project.md) for the generated layout.

`agentcli.LoadProject(root)` takes an immutable snapshot of project-owned
inputs:

```text
AGENTS.md
.agentcli/
├── config.yaml
├── MAIN.md
├── skill/
│   └── interview/SKILL.md
└── agent/
    └── researcher/researcher.md
```

`config.yaml` and `MAIN.md` are required. `AGENTS.md`, skill directories, and
subagent directories are optional. Invalid YAML, unknown frontmatter fields, missing provider profiles, unknown
skills, or unregistered tool allowlist entries cause initialization to fail.
This makes configuration mistakes visible before the first model request.

## Provider configuration

`.agentcli/config.yaml` owns connections, the initial permission mode, and the
optional per-parent open-subagent quota:

```yaml
permission_mode: default
max_subagents: 4

providers:
  primary:
    type: openai
    url: https://api.openai.com/v1
    api_key: ${API_KEY}
    request_timeout: 2m

  openrouter:
    type: openai
    url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    request_timeout: 90s
```

Provider names are application-defined aliases. `MAIN.md` and subagent files
refer to the alias, while the required `type` field selects the adapter. Both
`primary` and `openrouter` above use `type: openai`, so their names can change
without changing protocol behavior. `openai` is currently the only supported
type; missing or unsupported types fail during `LoadProject`.

`max_subagents` limits non-closed child instances per parent session. A positive
value sets the quota; omitting it or setting it to `0` keeps the default of 4.
Negative values are rejected. The Go option `WithMaxSubagents` can override the
project value when constructing an Agent.

Environment substitutions use `${NAME}`. A missing variable is a load error;
the loader does not silently send an empty credential.

## Main-agent definition

`.agentcli/MAIN.md` is always loaded, so it does not need `name` or
`description`:

```markdown
---
provider: primary
model: gpt-4.1-mini
skills:
  - interview
tools:
  - lookup_topic
  - publish_report
---

Understand the requested outcome, use capabilities deliberately, and provide a
clear self-contained result.
```

`skills` and `tools` are strict allowlists. Omit a key when the main agent gets
none of that capability. An explicit empty list is rejected to avoid confusing
"configured empty" state.

Listing a custom tool does not create its handler. The Go application must also
register that exact name with `agentcli.WithTool`; otherwise
`agentcli.New` returns an error such as:

```text
root agent requires custom tool "publish_report", but it is not registered
```

Registration makes a handler available to the application catalog; each agent
allowlist determines whether that model can see it. A required end-of-turn tool
is required only for agents whose allowlist exposes it. The generated
researcher intentionally exposes `glob` and `read`, not `edit` or
`report_discord`.

## Project instructions

`AGENTS.md` contains owner instructions shared with the model. When it exists,
project loading creates exactly two system messages:

1. One framework message containing runtime rules, environment/model context,
   `MAIN.md`, and discovery-only skill/subagent catalogs.
2. One project-owner message containing `AGENTS.md` verbatim.

Without `AGENTS.md`, only the grouped framework message is sent. Neither system message is persisted in conversation storage. They are rebuilt
for provider calls from the loaded project snapshot.

## Programmatic overrides

`WithProject` applies the loaded model, prompts, root identity, permission mode,
skills, and subagents. Later scalar options can override it:

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithPermissionMode(permission.CriticalOnly),
    agentcli.WithToolWorkers(8),
)
```

Use `WithProjectRoot` only when constructing without a loaded project. It sets
the identity used for project-scoped permission grants; it does not sandbox or
register tools.

## Configuration checklist

- Keep API keys in the process environment.
- Give every main/subagent tool an exact registered-name match.
- Keep tool allowlists minimal.
- Describe skills and subagents narrowly enough that the model can avoid
  unnecessary activation.
- Start in `default` while testing safety classifications.
