# Storage contracts

`storage.Message` is the canonical provider-neutral transcript value. It stores identity, session, turn, type, text, assistant reasoning, generic tool calls, and generic tool results. Reasoning is permitted only on assistant messages and is returned by the HTTP message representation. User/system/runtime-event content must be nonblank; tool calls/results have distinct validation rules. Never persist OpenAI SDK message types.

Storage interfaces cover messages, permissions, confirmations, and subagent relationships. Implementations must return independent copies rather than exposing internal slices or JSON buffers. Message append is ordered and atomic for one same-session batch, rejects duplicate message IDs, and supports turn-existence checks.

`storage/inmemory` provides synchronized process-local defaults. Its state disappears on process restart, so production callers that need durability should supply their own implementations through `agentcli` options while preserving the same validation and ordering contracts.

Back to [boundaries/index.md](index.md).
