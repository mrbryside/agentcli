# Start Subagent Always-Continue Plan

> Superseded: `start_subagent` now requires `continue_after_dispatch`. A false
> value conditionally ends a successful pending-callback batch, while true
> preserves the continue behavior described by this historical plan.

- [x] Change the `start_subagent` tool contract so it has no `finish_turn` input or output and always returns control to the parent turn.
- [x] Update orchestration tests to cover repeated sequential `start_subagent` calls and reject the removed argument.
- [x] Update the harness system prompt and subagent documentation to distinguish start behavior from send/force-close behavior.
- [x] Update the Discord main prompt to issue one start per delegated task and finish through `report_discord`.
- [x] Run focused tests, full tests, vet, race tests, and review the final diff.
