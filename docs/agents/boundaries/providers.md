# Provider boundaries

`provider.Provider[Request, Chunk]` starts a provider-specific chunk stream and parses chunks into generic `StreamEvent` values. The generic `provider.Stream` retains provider events, supports replaying subscriptions, folds immutable state, and exposes a final result.

`agentruntime.Model` is the runtime-facing abstraction. `agentruntime/modeladapter/openai` converts generic transcript messages, system prompts, context reminders, and tool definitions into the OpenAI-compatible request immediately before streaming. It maps trusted runtime events to a provider-legal input role, preserves tool-call/result correlation, and filters legacy blank text messages. When a transcript already ends in assistant output, an ephemeral context reminder is appended as a new user-role message. This preserves tool-call/result adjacency and prevents LiteLLM-compatible endpoints from rejecting repeated repair output as multiple trailing assistant messages.

Keep provider type, endpoint, API key, and timeout in a named project connection profile; model selection stays in the agent definition. Profile aliases do not select adapters—the required `type` discriminator does. New provider implementations should add a validated type and translate only at their boundary without leaking provider SDK types into runtime, storage, events, or tool domains.

Back to [boundaries/index.md](index.md).
