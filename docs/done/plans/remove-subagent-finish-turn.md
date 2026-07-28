# Remove Subagent Finish-Turn Plan

> Superseded in part: `start_subagent` now requires
> `continue_main_agent`. A false value conditionally ends a successful
> pending-result batch; `send_subagent_message` retains the passive
> always-continue runtime behavior.

- [x] Remove `finish_turn` from `send_subagent_message` and the legacy destructive-close schemas, handlers, results, and turn-behavior resolution.
- [x] Make every model-facing subagent management tool continue the main agent turn without exposing a `finish_turn` argument.
- [x] Simplify tool descriptions, system orchestration prompt, result instructions, and documentation to use application completion/finalizers.
- [x] Update regression and integration tests for the uniform always-continue contract.
- [x] Run focused tests, full tests, vet, race tests, and review all remaining `finish_turn` references.
