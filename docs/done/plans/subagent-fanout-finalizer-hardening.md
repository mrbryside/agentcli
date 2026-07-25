# Subagent fan-out and finalizer hardening

- [x] Scope `StartOrReuse` candidates to the requested subagent definition and cover cross-definition behavior.
- [x] Add `accepted` and `deduplicated` semantics to `start_subagent` results and cover created/reused/no-op cases.
- [x] Restrict required-finalizer repair rounds to the missing finalizer tools and cover distracting-tool behavior.
- [x] Align the Discord agent web-research prompt with explicit parallel instances and valid single-URL recovery.
- [x] Run focused and full test suites, review both diffs, and archive this plan.
