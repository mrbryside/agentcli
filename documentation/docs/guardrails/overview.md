---
title: Guardrails overview
sidebar_position: 1
---

# Guardrails overview

Guardrails add application-owned checks around input, final assistant output,
and successful custom-tool output. Every layer supports a Go callback. Input
and assistant-output guards also support prompt checks, while custom tools can
attach a prompt directly to their declaration.

| Boundary | Runs | Rejection behavior |
| --- | --- | --- |
| Input | After request normalization, before transcript persistence | `Agent.Start` returns `ErrInputGuardRejected`; no input message or run is created. |
| Assistant output | After the assistant message is persisted, before turn completion | Feedback is added as an ephemeral context reminder and the provider receives another round. |
| Custom-tool output | After a handler succeeds, before a successful tool result is published | Raw output is withheld; the runtime stores a failed tool result with feedback and starts another provider round. |

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
- mutable messages and raw JSON passed to callbacks are defensive copies.

## Guardrails are not a sandbox

Prompts and output checks do not replace tool permissions, confirmations,
argument validation, path scoping, or process isolation. In particular, a tool
output guard runs after its handler. If the handler changes external state,
that change already happened before the guard sees its output. Make retryable
tools idempotent, use idempotency keys for remote actions, and keep dangerous
validation before the side effect.

Assistant-output guards are repair guards, not secret-suppression filters. The
rejected assistant attempt is already in transcript storage and retained
provider events before evaluation. Use provider-side streaming controls or a
separate buffered presentation layer when unapproved tokens must never be
stored or shown.

Continue with [Agent input and output](agent-input-output.md),
[Tool-output guards](tool-output.md), and the
[Prompt verdict contract](prompt-contract.md).
