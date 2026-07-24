# Guardrails

Read this file when changing input/output policy checks, prompt-guard model
selection, tool-output validation, or retry feedback semantics.

| If you want to know... | Go to |
| --- | --- |
| Which boundary runs when and what rejection does | [Boundary lifecycle](#boundary-lifecycle) |
| How function and prompt guards are configured | [Configuration](#configuration) |
| How tool-output rejection reaches the model | [Tool feedback loop](#tool-feedback-loop) |
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

`agentruntime.ToolOutputGuard` runs in the executor after an application
handler returns valid JSON and before a successful tool result is emitted.
`ToolOutputReject` discards the raw output and emits `ToolResultFailed` with
feedback and `ContinueTurn`.

## Configuration

Root Agent options expose `WithInputGuard`, `WithOutputGuard`,
`WithInputGuardPrompt`, and `WithOutputGuardPrompt`. Function and prompt modes
are mutually exclusive per direction. Input/output prompt guards use the main
model unless `WithInputGuardProvider` or `WithOutputGuardProvider` selects a
provider profile and model from the loaded Project.

`toolexecution.Tool` exposes `ToolOutputGuard` and
`ToolOutputGuardPrompt`. A prompt-guarded tool may set
`ToolOutputGuardModel` to `*GuardModelConfig`; nil uses the Agent model.
`GuardModelConfig` groups the required provider and model strings. Agent
construction resolves explicit tool models through `Project.ModelFor`.
Direct executor users provide a fallback `Config.ToolOutputGuardModel` and an
optional `Config.ToolOutputGuardModelResolver`.

Prompt evaluation is one isolated model request with no tools,
`ToolChoiceNone`, a trusted policy system prompt, and a JSON-encoded candidate
as the user message. The verdict requires exactly `allowed`, `reason`, and
`feedback`.

## Tool feedback loop

The executor does not retry a handler directly. A rejected output becomes a
correlated failed tool result. AgentRuntime stores that result, starts the next
provider round, and lets the model choose corrected arguments and a new call
ID. A later success restores the tool's configured `ContinueTurn` or
`EndTurn`. Rejected required finalizers remain unsatisfied.

Guard callback panics/errors, invalid decisions, malformed prompt verdicts,
and invalid successful handler JSON also become failed tool results. The raw
candidate output is never published in the successful result field.

## Validation and failure posture

- Reject whitespace-only prompts and provider/model fields.
- Reject function/prompt combinations at the same boundary.
- Require both fields inside a non-nil `GuardModelConfig`.
- Reject a tool guard model config without a prompt guard.
- Resolve explicit provider profiles during Agent/executor construction.
- Reject unknown actions, reject decisions without feedback, and proceed
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
assistant message is already in transcript storage. Tool-output guards run
after the handler, so side effects have already occurred. Retryable tools must
be idempotent; dangerous preconditions, permissions, and confirmations belong
before handler side effects.

The generated `report_discord` tool is a network-free demonstration. Its
prompt guard checks message bounds, disclosure policy, and output/argument
consistency, including `skipReport: true` → `skipped` and omitted/false →
`reported`. It uses the main model fallback and returns rejection as finalizer
feedback.

Back to [tools-safety/index.md](index.md).
