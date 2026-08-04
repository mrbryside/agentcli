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
declined result continues so the model can request more work or report the
error. Shared tool channels must be buffered and are caller-owned; the runtime
never closes them.

Provider rounds are unlimited by default. `WithProviderStepLimit(n)` opts the
main and subagent turns into an agentic budget. When that budget is exhausted, the
coordinator enters a restricted finalization phase: ordinary work tools
disappear, required completion tools remain available, and a trusted reminder
asks the model to finish from existing results. Subagent finalization exposes no
tools and accepts one text-only final answer; its task state is `incomplete`.
A compliant required
`EndResponseScope` call therefore still executes at the final boundary. If the
model returns text while a required trigger remains missing, the existing
completion guard replays its bounded repair with only missing trigger tools;
it fails rather than silently ending after three no-progress repairs.
When no completion tool is required, the finalizer must summarize existing
results in text. A permitted completion tool may be followed by one final text
round; a blank finalizer response receives the deterministic work-limit
fallback. Finalization never restores ordinary work tools.
Unauthorized tool calls are stripped and never sent to the executor. The initial
finalizer, its repair rounds, and any final text round after a successful
non-terminal completion tool count in `RunResult.Steps`.

Trusted runtime input such as a subagent result can be queued on an active
run. The run never changes an in-flight provider request. It drains and
durably appends queued input immediately before the next provider round, or at
`AttemptComplete` and then starts a fresh provider round. If the run closes
before that boundary, injection is rejected so the host can use a result
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
tools. The exact tool definitions sent in each provider request are also a hard
runtime boundary: a completed call for any other tool is rejected before
`ToolCallRequested` or handler execution. The runtime has no provider-specific
tool-choice abstraction; repair behavior is expressed through prompts/reminders
and, when explicitly supplied by a completion guard, an allowlist. Invalid
guard decisions fail the run instead of silently weakening the boundary.

Malformed or truncated streamed tool arguments and calls for tools absent from
that provider request receive one bounded recovery round. The failed call is
not persisted, dispatched, or replayed. The recovery request exposes no tools
and asks for one final text response from the existing transcript and completed
results. If that response is also malformed or requests a tool, the run fails
instead of entering a recovery loop.

Prompt-backed input guards are one-shot model checks before the main provider loop. An allowed verdict enters the ordinary coordinator. A rejected verdict maps to `InputRespond`: the runtime creates a synthetic completed run, persists the original user message and guard-generated assistant response, and emits content/completion events without exposing tools or starting the main model. Result `InputReject` remains the hard admission path and creates no run or transcript.

`Run` owns one turn's event history, subscriber queues, state, controls, and final result. A terminal event is not externally done until its effects—including transcript persistence—finish; only then do `Done`, `Status`, `Result`, and subscriber closure expose completion. This prevents completion results from racing the final stored assistant message. Interruption cancels the provider, sends a turn-scoped tool interrupt, records synthetic interrupted results where needed, and terminates with `ErrRunInterrupted`. Keep session/turn/call correlation intact across every channel.

At the Agent facade, one accepted human main-agent turn opens one in-memory response
scope. Accepted subagent assignments increment that scope's result barrier.
Results first try to reserve an inline runtime input on a compatible active
turn; otherwise result continuations reserve and settle the matching
assignment. Both remain in the originating scope and may accept follow-up
assignments that reopen the barrier.
The scope starts ending when its last active turn reaches a completion boundary
with no pending results. It emits `PreEndScope`, runs final
`EndResponseScope` handlers, removes the scope, and emits `EndScope`. Task
records remain retained and resumable regardless of whether the latest result
was completed, incomplete, or failed; scope completion does not close them.
Successful `EndResponseScope` delivery remains represented by its tool-call and
tool-result records; the coordinator does not synthesize an assistant message
from tool arguments.

Pure transition and folding duties live in `state.go`, `transition.go`, `effect.go`, and `result.go`; orchestration belongs in `runtime.go`, `run.go`, and `router.go`.

## Tasks

The main-model framework catalog has one child-work tool: `task`. Foreground
calls wait for the child final response and return the framework's task-result
JSON in the same
main turn; independent same-batch calls occupy executor workers concurrently.
Task IDs are resumable only by their owning main session while retained and
not running or closed.

Background calls and `WithTaskForegroundWait` promotions persist delivery
identity before returning `running`. Agent owns exactly-once completion
injection/continuation. Child step-limit finalization has no tools and returns
partial text as `incomplete`; a child cannot invoke `task`.

Back to [architecture/index.md](index.md).
