# Runtime logging

Read this when changing console lifecycle logging, project log configuration,
repair diagnostics, payload redaction, or root/child logger ownership.

| If you want to know... | Go to |
| --- | --- |
| Where `logging.enabled` and `logging.level` are decoded and validated | [`project.go`](../../../project.go) |
| How project and programmatic loggers are selected | [`logging.go`](../../../logging.go) and [`options.go`](../../../options.go) |
| Which runtime, tool, compaction, terminal, and repair records are emitted | [`agentruntime/logging.go`](../../../agentruntime/logging.go) |
| Where response-scope start/end records are emitted | [`toolexecution/response_scope.go`](../../../toolexecution/response_scope.go) |

Omitting `logging` disables runtime console logging. A present mapping defaults
to `enabled: true` and `level: info`. Supported levels are `debug`, `info`,
`warn`, and `error`. `WithLogger` overrides project logging when applied after
`WithProject`; child agents share the selected root logger.

Info records cover turn, response-scope, subagent-close lifecycle, and
requested repair rounds. Debug adds completed provider content, tool
arguments/results, compaction, and scope/subagent details. Tool JSON is recursively redacted for
token/secret/password/authorization/API-key fields and bounded in size.
Reasoning and guard feedback are never written to runtime logs.
Successful canonical assistant persistence is a debug record without message
content; extraction, delivery, or persistence failures are error records.

Repair records distinguish `output_guard` from `completion_guard`, include the
one-based attempt, current provider-step count, and completion tool allowlist,
but deliberately omit context reminders and guard feedback. A failed or
exhausted guard emits `agent repair failed`, followed by the terminal run
failure record.

Back to [observability/index.md](index.md).
