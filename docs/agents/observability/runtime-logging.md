# Runtime logging

Read this when changing console lifecycle logging, project log configuration,
repair diagnostics, payload redaction, or root/subagent logger ownership.

| If you want to know... | Go to |
| --- | --- |
| Where `logging.enabled` and `logging.level` are decoded and validated | [`project.go`](../../../project.go) |
| How project and programmatic loggers are selected | [`logging.go`](../../../logging.go) and [`options.go`](../../../options.go) |
| Which runtime, tool, compaction, terminal, and repair records are emitted | [`agentruntime/logging.go`](../../../agentruntime/logging.go) |
| Where response-scope start/end records are emitted | [`toolexecution/response_scope.go`](../../../toolexecution/response_scope.go) |

Omitting `logging` disables runtime console logging. A present mapping defaults
to `enabled: true` and `level: info`. Supported levels are `debug`, `info`,
`warn`, and `error`. Project-managed and `WithLogLevel` records normally go to
stderr. While an interactive Terminal UI is attached, it captures up to 2,000
recent records and exposes them with `Ctrl+L` or `/logs` instead of letting them
interleave with the conversation. `WithLogLevel` provides the same managed
routing for programmatic configuration. `WithLogger` overrides project logging
when applied after `WithProject`; its handler remains caller-owned and is not
rerouted by the Terminal. Subagents share the selected main-agent logger.

Info records cover turn, response-scope, subagent-close lifecycle, and
requested repair rounds. Debug adds completed provider content, tool
arguments/results, compaction, and scope/subagent details. Tool JSON is recursively redacted for
token/secret/password/authorization/API-key fields and bounded in size.
Reasoning and guard feedback are never written to runtime logs.

Repair records distinguish `output_guard`, `completion_guard`, and
`provider_response`. They include the one-based attempt, current provider-step
count, and active tool allowlist but deliberately omit context reminders and
guard feedback. `provider_response` uses an empty allowlist for its one
text-only recovery after malformed/truncated arguments or a tool call absent
from the exact provider request. A failed or exhausted repair emits the
terminal run failure record.

Back to [observability/index.md](index.md).
