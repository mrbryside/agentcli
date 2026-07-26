# Remove Subagent Finish-Turn Plan

- [x] Remove `finish_turn` from `send_subagent_message` and the legacy destructive-close schemas, handlers, results, and turn-behavior resolution.
- [x] Make every model-facing subagent management tool continue the parent turn without exposing a `finish_turn` argument.
- [x] Simplify tool descriptions, system orchestration prompt, result instructions, and documentation to use application completion/finalizers.
- [x] Update regression and integration tests for the uniform always-continue contract.
- [x] Run focused tests, full tests, vet, race tests, and review all remaining `finish_turn` references.
