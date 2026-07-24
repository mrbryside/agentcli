---
title: Guardrails overview
sidebar_position: 1
---

# Guardrails overview

Guardrails add application-owned checks around input, final assistant output,
and model-requested custom-tool calls. Every layer supports a Go callback.
Input and assistant-output guards also support prompt checks, while custom
tools can attach a prompt directly to their declaration.

| Boundary | Runs | Rejection behavior |
| --- | --- | --- |
| Input callback | After request normalization, before transcript persistence | `InputReject` returns `ErrInputGuardRejected`; no input message or run is created. `InputRespond` creates a completed streamed turn containing the supplied response. |
| Input prompt | After request normalization, before the main model | A rejected verdict becomes a completed streamed turn; the user input and guard response are stored, but the main model and tools are not called. |
| Assistant output | After the assistant message is persisted, before turn completion | Feedback is added as an ephemeral context reminder and the provider receives another round. |
| Custom-tool call | After permission/confirmation admission, before handler execution | The handler is not called; the runtime stores a failed tool result with feedback and starts another provider round. |

Prompt guards use a one-shot model request with no tools and require one strict
JSON verdict. Code guards make deterministic rules, external policy services,
and application state easy to integrate. A prompt guard is useful when the
policy needs semantic judgment, but it adds model latency and cost.

## Failure posture

Guard configuration and verdicts are fail-closed:

- a callback error or panic fails input start, fails an assistant-output run,
  or becomes a failed tool result at the tool boundary;
- an unknown action, missing feedback, malformed model JSON, or contradictory
  verdict is rejected;
- prompt checks expose no tools and cannot directly execute application
  actions;
- a rejected input prompt uses only its validated `reason` as the assistant
  response and never forwards the rejected input to the main model;
- mutable messages and raw JSON passed to callbacks are defensive copies.

## Guardrails are not a sandbox

Prompt checks do not replace tool permissions, confirmations, argument
validation, path scoping, or process isolation. A rejected tool-call guard
prevents handler execution, but an allowed call still needs ordinary
authorization, validation, and idempotency.

Assistant-output guards are repair guards, not secret-suppression filters. The
rejected assistant attempt is already in transcript storage and retained
provider events before evaluation. Use provider-side streaming controls or a
separate buffered presentation layer when unapproved tokens must never be
stored or shown.

Continue with [Agent input and output](agent-input-output.md),
[Tool-call guards](tool-call.md), and the
[Prompt verdict contract](prompt-contract.md).
