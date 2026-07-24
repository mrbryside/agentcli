# Permissions and confirmations

Permissions answer whether declared capabilities may execute. Each request includes actions, risk, reason, details, and session/turn/call correlation. Modes are `default`, `acceptEdits`, `criticalOnly`, `dontAsk`, `plan`, and `unrestricted`; explicit deny rules win. Pending decisions are durable through storage and accept allow-once, allow-session, allow-project, or deny.

Confirmations are independent Yes/No gates for invocation-specific information. Permission mode—including unrestricted—does not bypass them. Yes runs the handler, No produces a declined tool result, and interruption or timeout records terminal state without running the handler.

Standard subagents evaluate the same permission policy and mode as their
parent. Permission and confirmation requests remain owned by the child
executor, but both lifecycles are emitted as parent-addressed events while the
requests remain in their durable stores for recovery. The parent UI resolves
them with `ResolveSubagentPermission` or `ResolveSubagentConfirmation`; a
completion callback is deliberately not used because the child cannot complete
while it is awaiting that decision.

The executor publishes at most one permission or confirmation prompt per session at a time. Other approval-requiring calls in that session remain deferred while unrelated calls and other sessions may still run. After a permission decision, deferred permission descriptions are evaluated again so an allow-session or allow-project grant can cover later calls without presenting redundant prompts. A confirmation belonging to the admitted call runs before the next deferred approval in that session.

The reference Terminal adds a global FIFO above those per-session queues. Root
and child permissions and confirmations share one visible oldest request, and
only a matching decision kind advances it.

Descriptors must validate and normalize model-controlled arguments before showing details. `WithNonInteractive(true)` is an independent executor flag, not a permission mode: policy evaluation still runs first, `allow` and `deny` stay unchanged, permission `ask` becomes `deny`, and every required confirmation becomes declined. It does not change `Agent.PermissionMode()` or emit a mode-change event. Consequently `criticalOnly` still allows low/medium risk but denies high-risk requests that would have asked, while `unrestricted` still allows permissions but cannot bypass confirmation. UIs may answer late because requests are tracked by IDs, but must submit every correlation field exactly.

Back to [tools-safety/index.md](index.md).
