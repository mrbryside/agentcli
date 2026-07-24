---
title: Prompt verdict contract
sidebar_position: 4
---

# Prompt verdict contract

Input, assistant-output, and tool-call prompt guards use the same strict
response shape:

```json
{
  "allowed": false,
  "reason": "A concise policy reason.",
  "feedback": "Actionable instructions for a compliant retry."
}
```

All three fields are required. `allowed` must be a boolean. Unknown fields,
multiple JSON values, a missing or null field, surrounding prose, malformed
JSON, or non-empty feedback on an allowed verdict fail validation. Markdown
JSON fences are tolerated, but the model is instructed to return only one
object.

For a rejected input, `reason` is returned with `ErrInputGuardRejected`. For a
rejected assistant output or tool call, `feedback` drives the repair loop. If a
rejecting model leaves feedback empty, the runtime falls back to reason and
then to a safe generic retry instruction.

## Model request isolation

Every prompt check is a separate provider request:

- no tools are included;
- the policy is a trusted system prompt;
- the candidate input, assistant message, or tool name/arguments are encoded
  in one user message;
- a final trusted user message tells the guard to decide
  immediately, minimize reasoning, and return a concise fail-closed verdict
  instead of continuing uncertain analysis;
- prompt checks do not enter AgentRuntime recursively and do not create a new
  conversation turn.

Input and assistant-output prompt guards can use independent project provider
profiles through `WithInputGuardProvider` and `WithOutputGuardProvider`.
Each tool-call prompt guard can set `ToolCallGuardModel` to one
`GuardModelConfig`; omitting the config uses the Agent model. Use a callback
guard when a tool needs a non-model policy service.

## Operational guidance

- Expect one extra model request per prompt check and per retry attempt.
- Ensure a shared model implementation supports concurrent `Start` calls when
  several tools can finish in parallel.
- Keep policies narrow and test both allow and reject examples.
- Do not put secrets in rejection feedback.
- Treat model unavailability or malformed verdicts as a policy failure, not as
  permission to bypass the guard.
- Prefer function guards for exact schemas, numeric bounds, signatures,
  authorization, and deterministic application rules.
