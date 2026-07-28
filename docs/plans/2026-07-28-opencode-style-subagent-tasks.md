# OpenCode-style subagent tasks implementation plan

> Design source:
> [`docs/specs/2026-07-28-opencode-style-subagent-tasks.md`](../specs/2026-07-28-opencode-style-subagent-tasks.md)
>
> Target release: breaking `agentcli` `v0.1.0`, followed by a Discord-agent
> dependency upgrade and deployment.

## Fixed decisions

- The main model gets one framework tool named `task`.
- A new task uses `agent`, `description`, `prompt`, and optional `background`.
- A resumed task uses `task_id`, `prompt`, and optional `background`.
- The provider-facing schema stays flat; runtime validation enforces new versus
  resume field combinations.
- Foreground is the default and returns the subagent's final output in the same
  main-agent tool call.
- `WithTaskForegroundWait(duration)` optionally promotes a foreground task to
  background after the duration. Omitting it waits without a framework timeout.
- Background completion is delivered exactly once by `Agent`; Terminal, HTTP,
  and Discord do not run their own result-continuation coordinators.
- The subagent's final assistant response is the result. There is no
  `report_subagent_result` tool or completion-report repair loop.
- Runtime state is `running`, `completed`, `incomplete`, or `error`.
  `incomplete` is runtime-owned and means provider-step finalization was used.
- Optional per-agent result contracts extract a message and application-only
  metadata from one final response. Contract metadata never enters provider
  context.
- Persisted subagent sessions and host-only `/subagents` management endpoints
  remain named subagents. “Task” names the model-facing execution protocol.
- No old model-facing compatibility tools or exported result-continuation API
  remain. Users requiring the old protocol stay on the last `v0.0.x` tag.

## Terra-high execution batches

| Batch | Terra-high owner | Tasks | Starts when |
| --- | --- | --- | --- |
| A | Core storage/runtime owner | 1–3 | Immediately, sequential within the batch |
| B | Tool/prompt owner | 4–5 | Task 3 passes |
| C | Background-delivery owner | 6 | Task 5 passes |
| D1 | HTTP/SSE owner | 7 | Task 6 public types compile |
| D2 | Terminal/init owner | 8 | Task 6 public types compile |
| D3 | Prompt/project owner | 9 | Task 5 public tool contract compiles |
| E | Harness integration owner | 10 | Tasks 6–9 pass |
| F | Harness docs/release owner | 11 | Task 10 passes |
| G1 | Discord runtime owner | 12 | Harness `v0.1.0` exists |
| G2 | Discord prompt/docs owner | 13 | Harness `v0.1.0` exists |
| H | Deployment verifier | 14 | Tasks 12–13 pass |

Tasks 7, 8, and 9 are parallel-safe because their file ownership does not
overlap. Tasks 12 and 13 are parallel-safe for the same reason. Do not split
Tasks 1–6 across simultaneous writers: `agent.go`, `subagent_manager.go`,
storage delivery identity, and result transport form one state machine.

## Task 1: Freeze task types and persisted delivery identity

**Terra-high owner:** Core storage/runtime owner  
**Depends on:** none

**Files:**

- Create: `task.go`
- Modify: `storage/subagent.go`
- Modify: `storage/inmemory/subagent.go`
- Modify: `storage/subagent_test.go`
- Modify: `storage/inmemory/subagent_test.go`
- Modify: `options.go`
- Modify: `agentcli_test.go`

- [x] Add failing tests in `storage/subagent_test.go` for cloning and validating
  an optional `TaskDelivery` containing `MainAgentTurnID` and `AssignmentID`.
- [x] Add failing tests in `storage/inmemory/subagent_test.go` proving an update
  replaces the latest delivery identity without changing the record's original
  `MainAgentTurnID`.
- [x] Run `rtk go test ./storage/...`; expect the new type/field assertions to
  fail to compile.
- [x] Add `TaskDelivery` to `storage/subagent.go`; pseudocode:
  `Subagent.ActiveTaskDelivery *TaskDelivery`, with deep clone and validation
  requiring both IDs together.
- [x] Update `storage/inmemory/subagent.go` so create/update/read/list preserve a
  cloned `ActiveTaskDelivery`.
- [x] Run `rtk go test ./storage/...`; expect PASS.
- [x] Add failing root tests for `TaskState`, `TaskResult`, and
  `WithTaskForegroundWait`: negative duration rejected, zero/omitted disabled,
  positive duration retained.
- [x] Create `task.go` with `TaskStateRunning`, `TaskStateCompleted`,
  `TaskStateIncomplete`, `TaskStateError`, plus:
  `TaskResult{TaskID, AgentName, State, Output, Error}`.
- [x] Add `taskForegroundWait time.Duration` to `config` and implement
  `WithTaskForegroundWait(duration)` in `options.go`.
- [x] Run `rtk go test ./...`; expect PASS.
- [x] Commit the storage/types slice with message
  `feat(task): define task state and delivery identity`.

## Task 2: Add optional final-result contracts and application metadata events

**Terra-high owner:** Core storage/runtime owner  
**Depends on:** Task 1

**Files:**

- Modify: `subagent_definition.go`
- Modify: `subagent_definition_test.go`
- Create: `task_result_contract.go`
- Create: `task_result_contract_test.go`
- Modify: `system_event.go`
- Modify: `system_event_test.go`

- [ ] Add failing definition tests for optional front matter:
  `result.message_field` plus named boolean/string metadata fields with
  `required`.
- [ ] Add failing definition tests rejecting an empty message field, duplicate
  metadata names, unsupported field types, and `required` without a field.
- [ ] Run `rtk go test . -run 'Test.*AgentDefinition|Test.*SubagentDefinition'`;
  expect FAIL.
- [ ] Add `AgentResultContract` and `AgentResultMetadataField` to
  `subagent_definition.go`; parse and normalize them without changing
  definitions that omit `result`.
- [ ] Add failing parser tests for ordinary final text, a valid contracted JSON
  response, missing message, missing required metadata, unknown metadata, and
  malformed JSON.
- [ ] Implement `parseTaskFinalResult(definition, text)` in
  `task_result_contract.go`; pseudocode:
  `no contract -> output=text`; `contract -> decode one object, extract
  message_field, validate metadata, return output + metadata`; never retry.
- [ ] Run the focused definition and contract tests; expect PASS.
- [ ] Add failing `system_event_test.go` coverage for
  `SystemTaskCompleted` and deep-cloned metadata.
- [ ] Extend `SystemEvent` with
  `TaskCompleted *TaskCompletedEvent`; include task/session/turn/agent/state and
  `map[string]any` metadata, and clone maps recursively.
- [ ] Run `rtk go test . -run 'Test.*(SystemEvent|TaskFinalResult)'`; expect PASS.
- [ ] Commit with message
  `feat(task): validate final result contracts`.

## Task 3: Make the manager execute foreground tasks and resume by task ID

**Terra-high owner:** Core storage/runtime owner  
**Depends on:** Tasks 1–2

**Files:**

- Modify: `subagent_manager.go`
- Modify: `subagent_manager_test.go`
- Modify: `subagent_result.go`
- Modify: `subagent_result_test.go`
- Modify: `subagent_reminder.go`
- Modify: `subagent_reminder_test.go`

- [ ] Add failing manager tests for a new foreground task that blocks until the
  child run finishes and returns its last non-empty final assistant response.
- [ ] Add failing tests proving two foreground executions can occupy separate
  manager instances without mailbox/result-continuation state.
- [ ] Add failing resume tests: same main session succeeds and keeps transcript;
  another main session fails; running/closed/unknown task IDs fail; supplying a
  new agent with `task_id` fails.
- [ ] Add failing cancellation coverage proving parent tool-context cancellation
  interrupts the child run and returns `TaskStateError`.
- [ ] Run `rtk go test . -run 'TestSubagentManager.*Task'`; expect FAIL.
- [ ] Add internal `TaskRequest` and
  `subagentManager.ExecuteTask(ctx, request)`; pseudocode:
  `task_id empty -> create`; `task_id set -> owner-check + idle-check`; start
  child turn; wait for `Run.Result`; parse final result contract; derive state.
- [ ] Reuse `createSubagent`, `startTurnLocked`, permission/confirmation
  forwarding, skills, tool allowlists, storage transcript, and Langfuse model
  wrapping; do not register response-scope assignment metadata for foreground.
- [ ] Preserve the original `MainAgentTurnID`; write `ActiveTaskDelivery` only
  for an execution that will deliver later.
- [ ] Stop auto-closing completed task sessions at response-scope end so a valid
  `task_id` remains resumable; keep explicit host close and interruption.
- [ ] Simplify `subagent_reminder.go` to show only resumable task identity and
  current running/idle state; remove result payloads and retry instructions.
- [ ] Run focused manager/result/reminder tests; expect PASS.
- [ ] Run `rtk go test -race . -run 'TestSubagentManager.*Task'`; expect PASS.
- [ ] Commit with message
  `feat(task): execute foreground subagent tasks`.

## Task 4: Replace main-model subagent tools with one `task` tool

**Terra-high owner:** Tool/prompt owner  
**Depends on:** Task 3

**Files:**

- Move/replace: `toolexecution/builtin_subagent.go`
- Modify: `toolexecution/builtin_framework_test.go`
- Modify: `toolexecution/registry_test.go`
- Modify: `subagent_tools.go`
- Modify: `subagent_tools_test.go`
- Modify: `agent.go`
- Modify: `agentcli_test.go`

- [ ] Add failing framework tests asserting the main catalog contains `task`
  and omits `start_subagent`, `send_subagent_message`, list, status, and wait
  tools.
- [ ] Add failing schema tests asserting a flat object with required `prompt`
  and optional `agent`, `description`, `task_id`, and `background`.
- [ ] Add failing handler tests for strict new/resume validation and the simple
  JSON result fields `task_id`, `agent`, `state`, `output`, and `error`.
- [ ] Run `rtk go test ./toolexecution . -run 'Test.*TaskTool'`; expect FAIL.
- [ ] Replace `SubagentToolBridge` with `TaskToolBridge`; pseudocode:
  `decode -> validate new/resume form -> build TaskRequest from Invocation ->
  controller.ExecuteTask -> marshal TaskResult`.
- [ ] Define `TaskToolName = "task"` in `subagent_tools.go`; remove compatibility
  constants for the old model tools.
- [ ] Bind `TaskToolBridge` during `Agent.New` and register it only in the main
  registry.
- [ ] Filter available agent definitions by permissions before appending their
  names/descriptions to the `task` tool description; denied agents must be
  absent.
- [ ] Keep `task` denied to subagents through existing subagent tool validation
  and filtered registries.
- [ ] Add a concurrency test using two task calls in one executor batch and a
  barrier; assert both handlers start before either finishes and worker count is
  the ceiling.
- [ ] Run focused tool/registry/root tests; expect PASS.
- [ ] Commit with message
  `feat(task): expose one model-facing task tool`.

## Task 5: Remove completion-report tooling and enforce text-only finalization

**Terra-high owner:** Tool/prompt owner  
**Depends on:** Task 4

**Files:**

- Delete: `toolexecution/builtin_subagent_result.go`
- Delete: `toolexecution/builtin_subagent_result_test.go`
- Delete: `subagent_result_guard.go`
- Modify/delete matching guard tests in `subagent_result_test.go`
- Modify: `subagent_prompt.go`
- Modify: `agent.go`
- Rewrite: `subagent_step_limit_test.go`
- Modify: `required_tool_guard_test.go`
- Modify: `agentruntime/runtime_test.go`

- [ ] Rewrite `subagent_step_limit_test.go` first: after one allowed agentic
  provider round, the next request has zero tools and returns partial final
  text as `TaskStateIncomplete`.
- [ ] Add a regression in `required_tool_guard_test.go` proving
  `report_subagent_result` is never registered or requested.
- [ ] Add a runtime regression where the provider attempts a tool call during
  finalization; assert it is suppressed and the deterministic text fallback is
  used once.
- [ ] Run the focused step-limit/guard/runtime tests; expect FAIL.
- [ ] Remove `NewSubagentReportTool`, `SubagentReport`, its completion guard, and
  `StepLimitFinalizationTools` injection from subagent assembly in `agent.go`.
- [ ] Shorten `subagentCompletionPrompt` to require one self-contained final
  response after domain work; do not mention reporting, status, parent, callback,
  or another final round.
- [ ] Keep validation rejecting application `EndResponseScope` tools for
  subagents.
- [ ] Reuse the existing no-completion-tool runtime path:
  `tools=[]`, text-only finalization, one deterministic fallback; do not add a
  task-specific repair loop.
- [ ] Run focused tests; expect PASS.
- [ ] Run `rtk go test ./agentruntime ./toolexecution .`; expect PASS.
- [ ] Commit with message
  `refactor(task): use final text as subagent result`.

## Task 6: Add background execution, promotion, and exact-once delivery

**Terra-high owner:** Background-delivery owner  
**Depends on:** Tasks 3 and 5

**Files:**

- Move/replace: `subagent_result.go` to `task_result.go`
- Move/replace: `subagent_result_test.go` to `task_result_test.go`
- Modify: `subagent_manager.go`
- Modify: `subagent_manager_test.go`
- Modify: `agent.go`
- Modify: `agentcli_test.go`
- Modify: `toolexecution/response_scope.go`
- Modify: `toolexecution/response_scope_test.go`
- Modify: `system_event.go`
- Modify: `system_event_test.go`

- [ ] Add failing manager tests for `background=true`: immediate
  `TaskStateRunning`, one persisted delivery identity, and one eventual terminal
  result.
- [ ] Add failing promotion race tests for completion before timeout, timeout
  before completion, cancellation during promotion, and duplicate promotion;
  assert exactly one foreground result or one background delivery.
- [ ] Add failing scope tests proving foreground tasks create no result barrier,
  while background/promoted tasks register one obligation before returning
  `running`.
- [ ] Add failing resume tests proving a background resume routes its result to
  the latest `ActiveTaskDelivery`, not the original main-agent turn.
- [ ] Run focused manager/scope tests; expect FAIL.
- [ ] Implement background and promotion in `ExecuteTask`; pseudocode:
  `start child`; `background -> register delivery + return running`;
  `foreground wait configured -> select child result vs timer`; on timer,
  atomically register delivery, mark promoted, return running.
- [ ] Replace the model-facing callback payload with
  `<task_result>` containing only task ID, agent, state, output, and error.
- [ ] Make `Agent` own the internal background-result coordinator:
  try injection at the next provider boundary; otherwise start one continuation
  turn; clients never call continuation methods.
- [ ] Publish `SystemTaskCompleted` with validated application-only metadata,
  but omit metadata from `<task_result>` and stored provider messages.
- [ ] Remove exported `SubscribeSubagentResults`,
  `TryInjectSubagentResult`, and `ContinueSubagentResultSubscribed`; keep
  host-only session inspection/close APIs.
- [ ] Ensure `Agent.Close` stops the internal coordinator after accepted
  terminal results are drained or cancelled.
- [ ] Run focused tests; expect PASS.
- [ ] Run `rtk go test -race ./...`; expect PASS.
- [ ] Commit with message
  `feat(task): deliver background task results exactly once`.

## Task 7: Remove client-owned result continuation from HTTP and SSE

**Terra-high owner:** HTTP/SSE owner  
**Depends on:** Task 6

**Files:**

- Modify: `server.go`
- Delete: `server_subagent_results.go`
- Modify: `server_turn_queue.go`
- Modify: `server_types.go`
- Modify: `server_session_stream.go`
- Modify: `server_scope_events.go`
- Modify: `server_subagents.go`
- Modify: `server_test.go`
- Modify: `server_subagents_test.go`
- Modify: `server_types_test.go`

- [ ] Add failing server tests asserting startup has no
  `WithServerAutoContinueSubagents` option or client result pump.
- [ ] Add failing SSE tests for one background task completion, reconnect from
  a cursor, and no duplicate synthetic `subagent_result` turn.
- [ ] Add failing endpoint tests proving existing `/subagents` routes still
  enforce ownership and manage persisted sessions only.
- [ ] Run `rtk go test . -run 'TestServer'`; expect FAIL.
- [ ] Remove `autoContinueSubagents`, `continueSubagentResults`,
  `serverTurn.result`, `ServerTurnSourceSubagentResult`, and
  `SubagentResultReference`.
- [ ] Let ordinary run events and `SystemTaskCompleted` surface task lifecycle;
  do not start a server-owned main-agent continuation.
- [ ] Retain `/subagents` create/read/messages/interrupt/close routes and label
  their response types as host session-management data.
- [ ] Update SSE cloning/serialization for `SystemTaskCompleted` metadata without
  putting it in provider messages.
- [ ] Run focused server tests; expect PASS.
- [ ] Commit with message
  `refactor(server): rely on runtime task delivery`.

## Task 8: Simplify Terminal, playground, and generated starter surfaces

**Terra-high owner:** Terminal/init owner  
**Depends on:** Task 6

**Files:**

- Modify: `terminal.go`
- Modify: `terminal_test.go`
- Modify: `playground/terminal/main.go`
- Create: `playground/terminal/main_test.go`
- Modify: `init/install.sh`
- Modify: `init_prompt_test.go`
- Modify: `init/templates/tool_report_discord.go`
- Modify: `init/templates/tool_report_discord_test.go`
- Modify: `.agentcli/MAIN.md`
- Modify: `.agentcli/agent/researcher/researcher.md`
- Modify: `README.md`

- [ ] Rewrite Terminal stubs/tests to remove result subscriptions,
  `runResultTurn`, and deferred client continuations.
- [ ] Add a failing Terminal test proving a foreground task remains within the
  current main turn and background completion is handled by `Agent`.
- [ ] Add failing command tests proving `/agents`, `/agent`, `/agent-status`, and
  `/close` still navigate/manage subagent sessions.
- [ ] Run focused Terminal tests; expect FAIL.
- [ ] Remove old result-pump methods from `terminalAgent` and Terminal startup/
  shutdown; retain permission, confirmation, scope, and system-event monitors.
- [ ] Update progress labels to show `task` without exposing task protocol fields
  or waiting narration.
- [ ] Update the playground and starter `MAIN.md`/researcher template to show one
  foreground `task` and multiple same-batch tasks for independent work.
- [ ] Remove callback/report wording from the Discord report-tool template while
  preserving its final response and secret-safety behavior.
- [ ] Run Terminal, playground, init, and template tests; expect PASS.
- [ ] Commit with message
  `refactor(terminal): adopt task-managed subagents`.

## Task 9: Simplify project prompts, discovery, and validation

**Terra-high owner:** Prompt/project owner  
**Depends on:** Tasks 4–5

**Files:**

- Modify: `main_prompt.go`
- Modify: `project.go`
- Modify: `project_test.go`
- Modify: `subagent_prompt.go`
- Modify: `subagent_definition_test.go`
- Modify: `required_skills_test.go`
- Modify: `main_definition.go`

- [ ] Add failing prompt snapshots/assertions requiring the words and fields
  `task`, foreground default, same-batch parallel tasks, and explicit
  `task_id` resume.
- [ ] Add failing negative assertions for `callback`, `continue_main_agent`,
  `accepted`, `result_progress`, `report_subagent_result`,
  `send_subagent_message`, polling, and simulated waiting.
- [ ] Add failing project validation tests proving a subagent cannot list
  `task` or any main-only `EndResponseScope` tool.
- [ ] Run focused project/prompt tests; expect FAIL.
- [ ] Replace `mainAgentSubagentToolPrompt` and `subagentDiscoveryPrompt` with
  short task selection/parallel/resume guidance.
- [ ] Render only permission-allowed agent names and descriptions into task
  discovery; keep tool/skill filtering and required-skill checks unchanged.
- [ ] Keep subagent prompts focused on domain assignment and final output;
  result-contract instructions are generated only for definitions declaring a
  contract.
- [ ] Run focused prompt/project/skill tests; expect PASS.
- [ ] Commit with message
  `refactor(prompt): describe subagents as tasks`.

## Task 10: Add end-to-end harness regressions for the observed failures

**Terra-high owner:** Harness integration owner  
**Depends on:** Tasks 6–9

**Files:**

- Rewrite: `subagent_integration_test.go`
- Modify: `tool_guard_integration_test.go`
- Modify: `required_tool_guard_test.go`
- Modify: `agentruntime/integration_test.go`
- Modify: `server_test.go`
- Modify: `terminal_test.go`

- [ ] Add a failing same-turn foreground scenario:
  main calls `task`, child returns final text, main receives one tool result,
  and no second main run exists.
- [ ] Add a failing parallel scenario with two independent task calls in one
  batch; assert both child runs overlap and results correlate to call IDs.
- [ ] Add a failing incident regression: child performs domain tools, reaches
  provider-step limit, gets one text-only finalization, and returns one
  `incomplete` task result.
- [ ] Assert the incident regression contains zero
  `report_subagent_result` calls, zero result-report repair rounds, and zero
  duplicate main fallback turns.
- [ ] Add failing background and auto-promotion scenarios asserting one trusted
  task-result input, one final response-scope delivery, and exact-once
  cancellation/error handling.
- [ ] Add failing contracted-result scenarios: valid metadata publishes one
  system event; invalid contract produces one task error and no metadata.
- [ ] Run focused integration tests; expect FAIL.
- [ ] Make only minimal production fixes required by the regressions; do not add
  compatibility translations to the old protocol.
- [ ] Run `rtk go test ./...`; expect PASS.
- [ ] Run `rtk go test -race ./...`; expect PASS.
- [ ] Run `rtk go vet ./...`; expect PASS.
- [ ] Commit with message
  `test(task): cover foreground and background orchestration`.

## Task 11: Update harness documentation, generated API artifacts, and release

**Terra-high owner:** Harness docs/release owner  
**Depends on:** Tasks 7–10

**Files:**

- Modify: `docs/agents/application/skills-and-subagents.md`
- Modify: `docs/agents/application/subagent-lifecycle.md`
- Modify: `docs/agents/application/client-surfaces.md`
- Modify: `docs/agents/application/agent-and-project.md`
- Modify: `docs/agents/architecture/runtime-flow.md`
- Modify: `docs/agents/tools-safety/tool-execution.md`
- Modify: `documentation/docs/capabilities/subagents.md`
- Modify: `documentation/docs/capabilities/subagent-lifecycle-control.md`
- Modify: `documentation/docs/agentcli/runs-and-sessions.md`
- Modify: `documentation/docs/agentcli/subagent-views.md`
- Modify: `documentation/docs/agentcli/server.md`
- Modify: `documentation/docs/terminal/subagent-views.md`
- Modify: `documentation/docs/api/go-api.md`
- Modify: `documentation/docs/api/http-api.md`
- Modify: `documentation/docs/api/sse-events.md`
- Modify: `documentation/docs/examples/api-client-integration.md`
- Modify: `documentation/docs/getting-started/project-configuration.md`
- Modify: `documentation/docs/use-cases/discord-bot.mdx`
- Modify: `documentation/docs/capabilities/context-compaction.md`
- Modify: `documentation/docs/capabilities/skills.md`
- Regenerate: `documentation/static/openapi/swagger.json`
- Regenerate: `documentation/static/openapi/swagger.yaml`
- Regenerate: `documentation/static/redoc/index.html`
- Modify: `AGENTS.md`

- [ ] Replace old lifecycle diagrams and examples with foreground, background,
  promotion, resume, result contract, and exact-once delivery examples.
- [ ] Document that `/subagents` is host session management while the main model
  sees only `task`.
- [ ] Document `WithTaskForegroundWait`, task states, result-contract metadata,
  task permission filtering, nesting denial, and the breaking API removals.
- [ ] Update `AGENTS.md`'s documented commit only after code/docs commits are
  final.
- [ ] Run `rtk go test ./...`, `rtk go test -race ./...`, and
  `rtk go vet ./...`; expect PASS.
- [ ] Run `rtk npm run build` in `documentation/`; expect OpenAPI generation,
  Redoc validation/rendering, and Docusaurus build to PASS.
- [ ] Review `rtk git diff --check` and generated artifact diffs.
- [ ] Move the completed plan to
  `docs/done/plans/opencode-style-subagent-tasks.md` only after Tasks 12–14 are
  complete; leave it under `docs/plans/` during cross-repository rollout.
- [ ] Commit documentation with message
  `docs(task): document foreground subagent tasks`.
- [ ] Push harness `main`, create and push breaking tag `v0.1.0`, and verify the
  tag resolves to the tested commit.

## Task 12: Migrate Discord runtime and preserve the requester guard

**Terra-high owner:** Discord runtime owner  
**Depends on:** Task 11 and harness tag `v0.1.0`

**Files in `/Users/sirawat/personal/discord-agent`:**

- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `internal/discord/discord_bot.go`
- Modify: `internal/discord/discord_bot_test.go`
- Modify: `internal/discord/session_manager.go`
- Modify: `internal/discord/session_manager_test.go`
- Modify: `internal/discord/discord_report_sink.go`
- Modify: `internal/discord/discord_report_sink_test.go`
- Modify: `cmd/discord-agent/main.go`
- Modify: `cmd/discord-agent/main_test.go`

- [ ] Update `go.mod` to `github.com/mrbryside/agentcli v0.1.0`; run
  `rtk go mod tidy`.
- [ ] Replace Discord bot test doubles for result subscription/injection/
  continuation with `SubscribeSystemEvents`.
- [ ] Add failing tests where `SystemTaskCompleted` from
  `discord-server-operator` sets `ExpectedUser` when
  `requires_requester_reply=true` and clears it when false.
- [ ] Add failing tests for invalid/error task completion clearing the guard,
  another user being rejected, and the original requester being accepted.
- [ ] Add failing progress tests proving one foreground task reuses the root
  progress message through `PreEndScope`/`EndScope`.
- [ ] Run `rtk go test ./internal/discord ./cmd/discord-agent`; expect FAIL.
- [ ] Remove `subscribeResultsFn`, `continueResultFn`, `tryInjectResultFn`, the
  async result pump, result-route assignment map, and pending-result fallback
  deferral from `discord_bot.go`.
- [ ] Observe `SystemTaskCompleted`; for the Discord operator, use the current
  root turn route to set/clear `ExpectedUser`. Do not expose metadata to prompts
  or Discord messages.
- [ ] Remove `resultRoutes`, `AcceptResult`, `PreserveResultRoute`, and
  `LookupResultRoute` from `session_manager.go`; retain ordinary turn bindings
  and `ExpectedUser`.
- [ ] Make `ReportSink.Send` use only `LookupTurn`; remove retained result-route
  fallback.
- [ ] Configure `WithTaskForegroundWait(15*time.Second)` in
  `cmd/discord-agent/main.go`; keep `WithProviderStepLimit(10)` and existing
  tool workers unless concurrency tests require an explicit higher limit.
- [ ] Run focused Discord tests; expect PASS.
- [ ] Commit with message
  `refactor(discord): consume runtime-managed tasks`.

## Task 13: Migrate Discord agents, skills, routing tests, and docs

**Terra-high owner:** Discord prompt/docs owner  
**Depends on:** Task 11 and harness tag `v0.1.0`

**Files in `/Users/sirawat/personal/discord-agent`:**

- Modify: `.agentcli/MAIN.md`
- Modify: `.agentcli/agent/discord-server-operator/discord-server-operator.md`
- Modify: `.agentcli/agent/web-summary/web-summary.md`
- Modify: `.agentcli/skill/discord-live-server/SKILL.md`
- Modify: `.agentcli/skill/discord-operator-followup/SKILL.md`
- Delete: `.agentcli/skill/discord-operator-result/SKILL.md`
- Modify: `.agentcli/skill/web-research/SKILL.md`
- Delete: `.agentcli/skill/web-summary-result/SKILL.md`
- Modify: `cmd/discord-agent/project_test.go`
- Modify: `docs/agents/application/prompts-and-routing.md`
- Modify: `docs/agents/application/runtime-and-config.md`
- Modify: `docs/agents/discord/bot-sessions-and-delivery.md`
- Modify: `docs/agents/discord/live-operations.md`
- Modify: `docs/agents/web/research-and-tools.md`
- Modify: `docs/agents/development/testing-and-compose.md`
- Modify: `AGENTS.md`

- [ ] Add failing project tests requiring a single `task` workflow and rejecting
  old result/callback/follow-up tool names and protocol fields.
- [ ] Add a failing definition test for the operator result contract:
  message field `message`, required boolean metadata
  `requires_requester_reply`.
- [ ] Run `rtk go test ./cmd/discord-agent -run TestProject`; expect FAIL.
- [ ] Simplify `MAIN.md` routing: choose domain work from descriptions, use one
  foreground task normally, emit multiple same-batch tasks for independent web
  research, and resume the recorded task ID for requester clarification.
- [ ] Configure the Discord operator's one final JSON response:
  natural `message` plus `requires_requester_reply`; use true only for one
  clarification requiring the original requester.
- [ ] Make `discord-live-server` call a foreground task and use its output in the
  same turn; make `discord-operator-followup` resume the task ID with fresh live
  Discord context.
- [ ] Remove result-only skills; task results no longer trigger separate skill
  routing events.
- [ ] Keep web search's required `web-research` skill and parallel independent
  reader guidance; web-summary returns ordinary final text without a result
  contract.
- [ ] Update the six named context docs and `AGENTS.md` to match runtime-managed
  tasks and the requester guard.
- [ ] Run focused project tests; expect PASS.
- [ ] Commit with message
  `refactor(prompt): route Discord work through tasks`.

## Task 14: Cross-repository verification, release, deployment, and Langfuse audit

**Terra-high owner:** Deployment verifier  
**Depends on:** Tasks 12–13

**Files in `/Users/sirawat/personal/discord-agent`:**

- Modify if needed: `docker-compose.yml`
- Modify if needed: deployment image/tag configuration referenced by Compose
- Move after success:
  `/Users/sirawat/personal/harness-api/docs/plans/2026-07-28-opencode-style-subagent-tasks.md`
  to
  `/Users/sirawat/personal/harness-api/docs/done/plans/opencode-style-subagent-tasks.md`

- [ ] Run `rtk go test ./...`, `rtk go test -race ./...`, and
  `rtk go vet ./...` in harness; expect PASS.
- [ ] Run `rtk npm run build` in harness `documentation/`; expect PASS.
- [ ] Run `rtk go test ./...`, `rtk go test -race ./...`, and
  `rtk go vet ./...` in Discord; expect PASS.
- [ ] Review both repositories with `rtk git status --short`,
  `rtk git diff --check`, and focused diffs; preserve unrelated user changes.
- [ ] Push Discord `main`, create/push its next version tag, and update the
  deployment tag consumed by Compose.
- [ ] Run `rtk docker compose build` and `rtk docker compose up -d`; verify all
  services are healthy.
- [ ] Exercise one foreground web task; verify the task result returns in the
  same main turn and Discord sends one final response.
- [ ] Exercise two independent web tasks in one batch; verify child runs overlap,
  results correlate, and one combined final response is sent.
- [ ] Exercise a Discord operator clarification; verify only the original
  requester may answer and the same task ID resumes.
- [ ] Exercise an auto-promoted/background task; verify one trusted result,
  one main continuation, and one Discord final response.
- [ ] Inspect Langfuse: no `report_subagent_result`, no report repair rounds, no
  repeated fallback provider errors, no progress-message storm, and exactly one
  terminal task result per assignment.
- [ ] Move this plan to `docs/done/plans/`, update harness `AGENTS.md`'s documented
  commit if the move creates a final docs commit, push that commit, and move the
  `v0.1.0` tag only if it has not already been published. Never rewrite a
  published tag; publish a follow-up patch tag instead.

## Self-check

- Every decision in the design spec maps to Tasks 1–14.
- Foreground execution, parallel tool calls, resume ownership, background
  promotion, exact-once result delivery, text-only step-limit finalization,
  optional result contracts, Discord requester ownership, public API removal,
  docs, release, and deployment each have explicit tests.
- The same names are used throughout:
  `task`, `TaskResult`, `TaskState`, `TaskDelivery`,
  `WithTaskForegroundWait`, `SystemTaskCompleted`, and `TaskCompletedEvent`.
- No task asks an implementation agent to recreate the old model protocol.
- Parallel ownership is limited to disjoint file sets after public contracts
  freeze.
