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
`ErrInputGuardRejected`.

`agentruntime.OutputGuard` runs after a terminal assistant message is stored
and before completion/finalizer checks. `OutputRetry` adds trusted ephemeral
feedback to the next model request and consumes another provider step.

`agentruntime.ToolCallGuard` runs in the executor after permission and
confirmation admission but before the application handler. `ToolCallReject`
prevents handler execution and emits `ToolResultFailed` with feedback and
`ContinueTurn`.

The former post-handler `ToolOutputGuard*` API is intentionally absent.
Handler-produced data must be validated by the handler before it returns.

## Configuration

Root Agent options expose `WithInputGuard`, `WithOutputGuard`,
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

Prompt evaluation is one isolated model request with no tools,
`ToolChoiceNone`, a trusted policy system prompt, and a JSON-encoded candidate
as the user message. The verdict requires exactly `allowed`, `reason`, and
`feedback`.

## Tool feedback loop

The executor does not retry a handler directly. A rejected tool call becomes a
correlated failed tool result without invoking the handler. AgentRuntime stores
that result, starts the next provider round, and lets the model choose corrected
arguments and a new call ID. A later success restores the tool's configured
`ContinueTurn` or `EndTurn`. Rejected required finalizers remain unsatisfied.

Guard callback panics/errors, invalid decisions, and malformed prompt verdicts
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
- Recover callback panics and translate them into the boundary's fail-closed
  error path.
- Decode prompt verdicts with unknown-field rejection and reject surrounding
  prose, multiple JSON values, or any missing/null required field.
- Pass callbacks defensive message/raw-JSON copies and do not trust callback
  mutation.

## Security limits

Guardrails are policy checks, not containment. Input guards do not replace
authorization. Assistant-output guards are repair checks because the rejected
assistant message is already in transcript storage. Tool-call guards run before
the handler, so rejected calls have no handler side effects. Allowed calls still
require appropriate permissions, confirmations, validation, and idempotency.

The generated `report_discord` tool is a network-free demonstration. Its
prompt guard checks the requested message, disclosure policy, and
`skipReport` decision before the handler can append to the report file. It
requires a direct result written as the reporting agent's own work and rejects
delegation, other-agent attribution, waiting language, and promised future
updates. It uses the main model fallback and returns rejection as finalizer
feedback.

Back to [tools-safety/index.md](index.md).
