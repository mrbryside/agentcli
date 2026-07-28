# Runtime flow

`agentruntime.Runtime` owns active-run registration and shared routing. One turn may be active per session; different sessions run concurrently. `Runtime.StartSubscribed` validates or generates the turn/message IDs, installs a live subscriber before `RunStarted`, persists the raw generic input, registers a `Run`, and starts its coordinator.

Immediately before each main-model round, an enabled compactor estimates the
provider-neutral request. If required, it summarizes and checkpoints the older
prefix before starting the main model. See
[compaction.md](compaction.md) for projection, storage, event, and failure
semantics.

The coordinator then repeatedly starts the configured `Model`, consumes
provider events, stages assistant candidates, persists tool-call messages,
sends correlated tool requests, waits for tool-result envelopes, and persists results. Successful
results normally start another provider round. The run completes without
another provider step only when the entire ordered batch succeeded and every
result has end-turn behavior; any continuing, failed, interrupted, denied, or
declined result continues so the model can dispatch more work or report the
error. Shared tool channels must be buffered and are caller-owned; the runtime
never closes them.

Provider rounds are unlimited by default. `WithProviderStepLimit(n)` opts the
main and child turns into an agentic budget. When that budget is exhausted, the
coordinator enters a restricted finalization phase: ordinary work tools
disappear, required trigger tools remain available, and a trusted reminder asks
the model to finish from existing results. A compliant required
`EndResponseScope` call therefore still executes at the final boundary. If the
model returns text while a required trigger remains missing, the existing
completion guard replays its bounded repair with only missing trigger tools;
it fails rather than silently ending after three no-progress repairs.
Unauthorized tool calls are stripped and never dispatched. The initial
finalizer and its repair rounds count in `RunResult.Steps`.

Trusted runtime input such as a subagent callback can be queued on an active
run. The run never changes an in-flight provider request. It drains and
durably appends queued input immediately before the next provider round, or at
`AttemptComplete` and then starts a fresh provider round. If the run closes
before that boundary, injection is rejected so the host can use a callback
continuation turn instead.

Provider completion without tools first stages its assistant output in `Run`
memory and produces an internal `AttemptComplete` effect. Output and completion
guards receive a defensive inspection snapshot containing that candidate, but
the candidate is neither in `MessageStorage` nor in the next provider request.
A retry discards it; `CompletionProceed` (or the absence of a guard) persists
it immediately before `RunCompleted`. Terminal tool batches remain durable
before completion inspection. A configured completion guard may request another
provider round with ephemeral reminders and an optional tool allowlist. A
non-nil empty allowlist is distinct from nil and deliberately exposes zero
tools. The runtime has no provider-level tool-choice abstraction; repair
behavior is expressed through prompts/reminders and, when explicitly supplied
by a completion guard, an allowlist. Invalid guard decisions fail the run
instead of silently weakening the boundary.

Prompt-backed input guards are one-shot model checks before the main provider loop. An allowed verdict enters the ordinary coordinator. A rejected verdict maps to `InputRespond`: the runtime creates a synthetic completed run, persists the original user message and guard-generated assistant response, and emits content/completion events without exposing tools or starting the main model. Callback `InputReject` remains the hard admission path and creates no run or transcript.

`Run` owns one turn's event history, subscriber queues, state, controls, and final result. A terminal event is not externally done until its effects—including transcript persistence—finish; only then do `Done`, `Status`, `Result`, and subscriber closure expose completion. This prevents completion callbacks from racing the final stored assistant message. Interruption cancels the provider, sends a turn-scoped tool interrupt, records synthetic interrupted results where needed, and terminates with `ErrRunInterrupted`. Keep session/turn/call correlation intact across every channel.

At the Agent facade, one accepted human root turn opens one in-memory response
scope. Accepted subagent dispatches increment that scope's callback barrier.
Callbacks first try to reserve an inline runtime input on a compatible active
turn; otherwise callback continuations reserve and settle the matching
dispatch. Both remain in the originating scope and may accept follow-up
dispatches that reopen the barrier.
The scope starts ending when its last active turn reaches a completion boundary
with no pending callbacks. It emits `PreEndScope`, then runs cleanup before
final `EndResponseScope` handlers:
completed/failed children touched only by that scope close automatically,
incomplete or cross-scope children remain open, and successful closes become a
one-shot trusted reminder reserved for the next human root turn. After all
final handlers run and the scope is removed, it emits `EndScope`.
Successful `EndResponseScope` delivery remains represented by its tool-call and
tool-result records; the coordinator does not synthesize an assistant message
from tool arguments.

Pure transition and folding duties live in `state.go`, `transition.go`, `effect.go`, and `result.go`; orchestration belongs in `runtime.go`, `run.go`, and `router.go`.

Back to [architecture/index.md](index.md).
