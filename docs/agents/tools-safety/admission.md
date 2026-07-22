# Permissions and confirmations

Permissions answer whether declared capabilities may execute. Each request includes actions, risk, reason, details, and session/turn/call correlation. Modes are `default`, `acceptEdits`, `criticalOnly`, `dontAsk`, `plan`, and `unrestricted`; explicit deny rules win. Pending decisions are durable through storage and accept allow-once, allow-session, allow-project, or deny.

Confirmations are independent Yes/No gates for invocation-specific information. Permission mode—including unrestricted—does not bypass them. Yes runs the handler, No produces a declined tool result, and interruption or timeout records terminal state without running the handler.

Descriptors must validate and normalize model-controlled arguments before showing details. Non-interactive Agents deny permission prompts and decline confirmations rather than waiting. UIs may answer late because requests are tracked by IDs, but must submit every correlation field exactly.

Back to [tools-safety/index.md](index.md).
