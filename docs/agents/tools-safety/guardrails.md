# Guardrails

Read this file when changing input/output policy checks, prompt-guard model
selection, tool-call validation, or retry feedback semantics.

| If you want to know... | Go to |
| --- | --- |
| Which boundary runs when and what rejection does | [Boundary lifecycle](#boundary-lifecycle) |
| How function and prompt guards are configured | [Configuration](#configuration) |
| How tool-call rejection reaches the model | [Tool feedback loop](#tool-feedback-loop) |
| Which failures must remain closed | [Validation and failure posture](#validation-and-failure-posture) |
| Which security limitations callers must understand | [Security limits](#security-limits) |

## Boundary lifecycle

`agentruntime.InputGuard` runs after request normalization and before message
persistence or `Run` creation. It accepts, rejects, or replaces content while
preserving normalized identity, timestamp, and message type. Rejection wraps
`ErrInputGuardRejected`. `InputRespond` instead creates a synthetic completed
run, persists the original user message plus the supplied assistant response,
and emits ordinary provider content/completion events without starting the main
model or tools. Prompt-backed input guards map a rejected verdict to
`InputRespond`, using the verdict reason as the user-facing response.

`agentruntime.OutputGuard` runs against a terminal assistant candidate held in
`Run` memory and before completion/trigger tool checks. Its defensive message
snapshot includes the candidate for inspection. `OutputRetry` discards the
candidate, adds trusted ephemeral feedback to the next model request, and
consumes another provider step. `OutputProceed` allows completion admission to
persist it.

`agentruntime.ToolCallGuard` runs in the executor after permission and
confirmation admission but before the application handler. `ToolCallReject`
prevents handler execution and emits `ToolResultFailed` with feedback and
a continuing result.

The former post-handler `ToolOutputGuard*` API is intentionally absent.
Handler-produced data must be validated by the handler before it returns.

## Configuration

Main-agent options expose `WithInputGuard`, `WithOutputGuard`,
`WithInputGuardPrompt`, and `WithOutputGuardPrompt`. Function and prompt modes
are mutually exclusive per direction. Input/output prompt guards use the main
model unless `WithInputGuardProvider` or `WithOutputGuardProvider` selects a
provider profile and model from the loaded Project.

`toolexecution.Tool` exposes `ToolCallGuard` and
`ToolCallGuardPrompt`. A prompt-guarded tool may set
`ToolCallGuardModel` to `*GuardModelConfig`; nil uses the Agent model.
`GuardModelConfig` groups the required provider and model strings. Agent
construction resolves explicit tool models through `Project.ModelFor`.
Direct executor users provide a fallback `Config.ToolCallGuardModel` and an
optional `Config.ToolCallGuardModelResolver`.

Prompt evaluation is one isolated model request with no tools, a trusted
policy system prompt, a JSON-encoded candidate user message, and a final trusted
user message with response rules requesting an immediate, concise decision
with minimal reasoning. Rejected input prompts require a complete concise
user-facing `reason`; this is streamed and persisted directly rather than sent
through the main model. The
verdict requires exactly `allowed`, `reason`, and `feedback`. Each prompt guard
evaluation is bounded by a 30-second timeout by default; set
`WithToolCallGuardTimeout` or `Config.ToolCallGuardTimeout` to change it. A
timeout fails the tool call before the handler runs, so the agent can retry.

## Tool feedback loop

The executor does not retry a handler directly. A rejected tool call becomes a
correlated failed tool result without invoking the handler. AgentRuntime stores
that result, starts the next provider round, and lets the model choose corrected
arguments and a new call ID. A later success restores the tool's configured
trigger or end-on-success behavior. Rejected `EndTurn` and `EndResponseScope`
trigger tools remain unsatisfied; rejected `EndTurnOnSuccess` tools do not end
the turn.

Guard result panics/errors, invalid decisions, and malformed prompt verdicts
also become failed tool results without invoking the handler. Invalid JSON from
an allowed handler remains a failed tool result.

## Validation and failure posture

- Reject whitespace-only prompts and provider/model fields.
- Reject function/prompt combinations at the same boundary.
- Require both fields inside a non-nil `GuardModelConfig`.
- Reject a tool guard model config without a prompt guard.
- Resolve explicit provider profiles during Agent/executor construction.
- Reject unknown actions, reject decisions without feedback, and allow
  decisions containing feedback.
- Recover result panics and translate them into the boundary's fail-closed
  error path.
- Decode prompt verdicts with unknown-field rejection and reject surrounding
  prose, multiple JSON values, or any missing/null required field.
- Pass results defensive message/raw-JSON copies and do not trust result
  mutation.

## Security limits

Guardrails are policy checks, not containment. Input guards do not replace
authorization. Assistant-output guards are repair checks; rejected candidates
remain observable in run events and diagnostics but do not enter transcript
storage or later model context. Tool-call guards run before
the handler, so rejected calls have no handler side effects. Allowed calls still
require appropriate permissions, confirmations, validation, and idempotency.

The generated `report_discord` tool is a network-free demonstration. Its
prompt guard checks the requested message, disclosure policy, and
`skipReport` decision before the handler can append to the report file. It
requires progress or results to be written as the reporting agent's own work
and rejects delegation, other-agent attribution, waiting language, and promised
future updates. Useful ongoing progress is valid reportable content: rejection
feedback preserves it, removes internal attribution, and suggests a concrete
direct rewrite instead of `skipReport`. The skip option is reserved for a
submitted message with no meaningful user-facing action, progress, status,
finding, or conclusion. It uses the main model fallback and returns rejection
as trigger tool feedback.

Back to [tools-safety/index.md](index.md).
