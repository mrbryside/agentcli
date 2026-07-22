# Tool execution

`toolexecution.Tool` combines a provider-neutral definition, JSON handler, optional permission descriptor, optional policy-aware permission descriptor, and optional confirmation descriptor. `Registry.Register` requires unique names, handlers, and object JSON schemas. `agentcli.WithCustomTool` is the preferred typed wrapper: it infers schema, strictly decodes arguments, invokes a typed handler, and encodes output.

`Executor.Run` consumes runtime requests, applies admission, dispatches accepted work through a bounded worker pool, emits correlated results, and consumes exact-turn interrupts. Calls are keyed by session, turn, and call ID. Preserve these identities and never let one session's interrupt or result affect another.

Framework tools (`load_skill` and root-only subagent tools) are owned by `toolexecution` and wired by `agentcli`. Application tools remain caller-owned; the framework does not silently register filesystem or shell tools.

Back to [tools-safety/index.md](index.md).
