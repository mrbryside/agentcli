# Prompt and tool-description ownership

Read this when changing any model-facing instruction, context reminder, built-in
tool description, or project prompt.

## Ordered prompt surfaces

The main agent receives separate system messages in this order:

1. Core runtime context, operating principles, tool-result discipline, secret
   safety, and response contract from `main_prompt.go`.
2. Skill discovery and loading rules from `project.go`, when skills exist.
3. Task protocol and the configured agent catalog from `main_prompt.go` and
   `project.go`, when subagents exist.
4. Application-owned `.agentcli/MAIN.md` instructions.

A subagent instead receives the framework prompt from `subagent_prompt.go`,
followed by its definition body as a separate assignment-role message. An exact
JSON result format appears only when that definition explicitly configures a
result contract.

Trusted transient instructions remain outside stored conversation history:
`turn_reminder.go` marks a new turn, `subagent_reminder.go` reports only active
background delivery, and runtime guard/finalization reminders describe a
specific retry boundary. Compaction uses an isolated summarizer prompt and
treats historical text as untrusted data.

## Policy versus protocol

Framework prompts and the `task` tool describe two capabilities:

- create separate work with `agent`, `description`, and `prompt`;
- optionally continue retained work with its exact `task_id` and a new
  `prompt`.

The framework does not decide that a user answer, correction, or related topic
must resume an earlier task. `.agentcli/MAIN.md` or a loaded skill owns that
domain policy. Presence of `task_id` remains an authoritative protocol choice:
create-only fields cannot retarget it, and an unknown, closed, or running ID
returns `task_not_found`, `task_closed`, or `task_running` without creating a
replacement.

Task-result wording must use the exact model-visible states `running`,
`completed`, `incomplete`, and `error`. Completed, incomplete, and error-result
tasks remain available for optional later continuation until the host closes
them.

## Tool-description sources

The framework owns two built-in descriptions:

| Tool | Description owner |
| --- | --- |
| `load_skill` | `toolexecution/builtin_skill.go`; mirrors skill discovery, once-per-turn loading, cached instructions, and unchanged tool availability. |
| `task` | `toolexecution/builtin_subagent.go`; mirrors create/resume fields, exact-ID errors, foreground/background behavior, batching, and no polling. |

`Registry.Register` appends required-skill and trigger/turn-ending guidance to
a cloned application description. The application retains ownership of the
tool's domain purpose and argument semantics.

The bootstrap templates own the model descriptions for `glob`, `read`, `edit`,
and `report_discord`; the Terminal playground separately owns `glob`, `read`,
and `confirm_demo`. Keep each description aligned with its schema, bounds,
permission/confirmation behavior, handler result, and trigger mode.

## Consistency checks

When editing these contracts:

- update the prompt and built-in description together when they expose the same
  protocol;
- keep application decisions out of framework prompts;
- keep optional result JSON conditional on an explicit result contract;
- use exact state, error-code, field, and trigger names;
- test positive capability wording and negative stale/over-prescriptive wording;
- run `go test ./...`, `go test -race ./...`, and `make docs-build`.

Back to [application/index.md](index.md).
