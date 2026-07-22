# Events and history

Agent events are runtime activity, not conversation storage. A `Run` retains ordered `AgentEvent` values with sequence numbers. `Run.Subscribe` is live-only and returns a cursor fencing the retained prefix; recover missed events with `Run.EventsBetween` before consuming later live events. `StartSubscribed` is the normal race-free way to observe a new run from its first event.

Provider `Stream.Subscribe` has different semantics: it replays the provider stream's current event history. Do not apply provider replay assumptions to agent runs.

Conversation history comes from `MessageStorage` or `Agent.ListMessages`. It contains generic system, user, trusted runtime-event, assistant, tool-call, and tool-result records. Resume a session by starting a new turn with the same session ID; provider adapters transform the stored generic history only at the provider boundary.

The Echo server adds retained HTTP/SSE recovery and a bounded FIFO for same-session turns above the runtime's strict single-active-turn rule. Session SSE uses one cursor across user and automatic subagent callback turns.

Back to [architecture/index.md](index.md).
