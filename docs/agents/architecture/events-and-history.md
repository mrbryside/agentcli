# Events and history

Agent events are runtime activity, not conversation storage. A `Run` retains ordered `AgentEvent` values with sequence numbers. `Run.Subscribe` is live-only and returns a cursor fencing the retained prefix; recover missed events with `Run.EventsBetween` before consuming later live events. `StartSubscribed` is the normal race-free way to observe a new run from its first event.

Provider `Stream.Subscribe` has different semantics: it replays the provider stream's current event history. Do not apply provider replay assumptions to agent runs.

Conversation history comes from `MessageStorage` or `Agent.ListMessages`. It
contains generic system, user, trusted runtime-event, assistant, tool-call,
tool-result, and (when enabled) compaction-checkpoint records. Checkpoints are
append-only cumulative summaries; they do not replace or delete the original
transcript. Provider requests project the newest checkpoint plus a recent
verbatim tail, so a resumed session continues to use an existing checkpoint
even if automatic compaction is subsequently disabled. Provider adapters
transform this projected generic history only at the provider boundary. See
[compaction.md](compaction.md) for checkpoint boundary and request-projection
rules.

When compaction needs to summarize, the run emits `compaction_started` before
the separate summarizer begins, then `compaction_completed` after its checkpoint
is persisted. A preparation, summarizer, or checkpoint-persistence failure
emits `compaction_failed` with `Error` and fails the run before the main model
starts that provider round. Summary text is not emitted as assistant content or
ordinary provider events.

The HTTP message views retain the checkpoint's `compaction_checkpoint` type but
deliberately omit its summary and coverage boundaries. Clients must treat these
records as opaque runtime state rather than displayable transcript content.

The Echo server adds retained HTTP/SSE recovery and a bounded FIFO for
same-session turns above the runtime's strict single-active-turn rule. Session
SSE uses one cursor across user turns, automatic subagent callback turns, and
the scope-level `PreEndScope`/`EndScope` events forwarded by the Agent facade.
The server waits until the triggering turn's runtime events are published
before forwarding either scope event, so `run_completed` remains ordered before
`pre_end_scope`.

Back to [architecture/index.md](index.md).
