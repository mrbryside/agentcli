# Provider boundaries

`provider.Provider[Request, Chunk]` starts a provider-specific chunk stream and parses chunks into generic `StreamEvent` values. The generic `provider.Stream` retains provider events, supports replaying subscriptions, folds immutable state, and exposes a final result.

`agentruntime.Model` is the runtime-facing abstraction. `agentruntime/modeladapter/openai` converts generic transcript messages, system prompts, context reminders, and tool definitions into the OpenAI-compatible request immediately before streaming. It maps trusted runtime events to a provider-legal input role, preserves tool-call/result correlation, and filters legacy blank text messages. Ephemeral repair reminders are appended as provider-legal user-role messages. Rejected assistant candidates are not part of the projected transcript, which preserves tool-call/result adjacency and avoids repeated trailing assistant drafts at OpenAI-compatible endpoints.

Optional `ModelMetadataProvider` supplies provider-neutral context-window and
maximum-output metadata for models that need capability-aware features such as
compaction. Optional `ContextEstimatorProvider` lets each main model select the
estimator for its own provider. `GenericContextEstimator` is the deterministic
fallback and conservatively estimates every generic request surface, charging
non-ASCII UTF-8 bytes conservatively for multilingual text. Provider-specific
adapters may provide a more exact estimator without exposing SDK types in
runtime.

When an adapter can identify a context-window rejection, it wraps
`agentruntime.ErrContextWindowExceeded`. The runtime may then force one
provider-neutral compaction and retry once. The OpenAI-compatible adapter
recognizes standard context-length codes and conservative HTTP 400 message
shapes used by compatible gateways. Other adapters should normalize their own
structured error into the same sentinel rather than adding provider checks to
the runtime.

The OpenAI-compatible adapter can resolve known aliases for directly
constructed adapters. Explicit limits belong to exact-name model entries under
each provider profile and apply only when that name is selected. Without them,
distinct main, child, and summarizer models resolve limits from their provider
`/models` endpoint first, then models.dev, then deterministic defaults of
122,880 context tokens and 66,560 output tokens.
Applications can still construct a custom adapter with
`openai.Config.MetadataResolver` and pass it through
`agentcli.WithModel` or `agentcli.WithCompactionModel`. An application can pass a
provider-aware `agentruntime.ContextEstimator` through
`agentcli.WithContextEstimator` when its adapter can estimate more exactly than
the conservative generic estimator. This is especially important when
compaction is enabled. Direct custom adapters that bypass project loading must
still expose valid metadata.

Keep provider type, endpoint, API key, and timeout in a named project connection profile; model selection stays in the agent definition. Profile aliases do not select adapters—the required `type` discriminator does. New provider implementations should add a validated type and translate only at their boundary without leaking provider SDK types into runtime, storage, events, or tool domains.

The optional per-model `reasoning` boolean is translated only at the OpenAI
boundary to `chat_template_kwargs.enable_thinking`. Arbitrary provider-specific
wire fields belong in that model's `extra_body`, which is cloned with the
provider and merged into the encoded top-level request JSON. Extra-body values
win on key collisions. Selection depends only on an exact model-name match,
not on provider-specific inference.

Back to [boundaries/index.md](index.md).
