# OpenCode-style compaction and Discord callback routing

- [x] Task 1: Replace chunked compaction with a single-call checkpoint containing a structured summary and serialized recent context.
  - Depends on: none
  - Verify: focused compactor and runtime compaction tests
- [x] Task 2: Update checkpoint storage/projection, prompt behavior, documentation, and compatibility tests.
  - Depends on: Task 1
  - Verify: full harness test suite, race tests, vet, documentation build
- [x] Task 3: Re-parent reused subagent results to their latest assignment and reuse callback progress activity.
  - Depends on: none
  - Verify: focused Discord routing/progress tests
- [x] Task 4: Update Discord docs and run full cross-repository verification.
  - Depends on: Task 2, Task 3
  - Verify: full Discord test suite, race tests, vet, Compose build/start and log smoke test
