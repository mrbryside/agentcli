# Runtime flow

`agentruntime.Runtime` owns active-run registration and shared routing. One turn may be active per session; different sessions run concurrently. `Runtime.StartSubscribed` validates or generates the turn/message IDs, installs a live subscriber before `RunStarted`, persists the raw generic input, registers a `Run`, and starts its coordinator.

The coordinator repeatedly starts the configured `Model`, consumes provider events, persists assistant/tool-call messages, sends correlated tool requests, waits for tool-result envelopes, and persists results. Successful results normally start another provider round. A tool with `EndTurn` behavior instead completes the run after the entire ordered result batch is stored; failed, interrupted, denied, and declined results still continue so the model can report the error. Shared tool channels must be buffered and are caller-owned; the runtime never closes them.

`Run` owns one turn's event history, subscriber queues, state, controls, and final result. A terminal event is not externally done until its effects—including transcript persistence—finish; only then do `Done`, `Status`, `Result`, and subscriber closure expose completion. This prevents completion callbacks from racing the final stored assistant message. Interruption cancels the provider, sends a turn-scoped tool interrupt, records synthetic interrupted results where needed, and terminates with `ErrRunInterrupted`. Keep session/turn/call correlation intact across every channel.

Pure transition and folding duties live in `state.go`, `transition.go`, `effect.go`, and `result.go`; orchestration belongs in `runtime.go`, `run.go`, and `router.go`.

Back to [architecture/index.md](index.md).
