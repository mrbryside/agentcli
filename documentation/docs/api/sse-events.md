---
title: Events
sidebar_position: 2
---

# Events

Root and child run event endpoints use standard Server-Sent Events.

Both endpoints are also present in the generated
[API documentation](/api-reference) under **Event streams**:

| Scope | Endpoint | Cursor |
| --- | --- | --- |
| Whole root session | `GET /v1/sessions/{sessionID}/events` | Monotonic across root turns. |
| Root turn | `GET /v1/sessions/{sessionID}/turns/{turnID}/events` | Per turn. |
| Subagent turn | `GET /v1/sessions/{parentSessionID}/subagents/{subagentID}/turns/{turnID}/events` | Per child turn. |

```bash
curl -NsS \
  http://127.0.0.1:8080/v1/sessions/demo/turns/TURN_ID/events
```

If the requested root turn is still queued, the HTTP connection waits without
starting the model. Headers and normal run events begin when FIFO admission
starts that turn. Cancelling the queued turn wakes the connection with a
`turn_cancelled` API error if the response has not begun.

## Session stream

Use the whole-session endpoint for interactive applications. It replays and
follows queued lifecycle records, ordinary user turns, and parent turns created
automatically when subagents complete.

```text
id: 19
event: provider_event_received
data: {"cursor":19,"type":"turn_event","source":"subagent_callback","session_id":"demo","turn_id":"turn_callback","turn_url":"/v1/sessions/demo/turns/turn_callback","events_url":"/v1/sessions/demo/turns/turn_callback/events","subagent_callback":{"subagent_id":"subagent_123","display_name":"Sol","definition_name":"researcher","child_session_id":"subagent-session","child_turn_id":"turn_child","status":"completed"},"runtime_event":{"sequence":2,"session_id":"demo","turn_id":"turn_callback","type":"provider_event_received","provider_event":{"type":"content_received","content":"The research found..."}}}
```

Session lifecycle types are `turn_queued`, `turn_admitted`, `turn_cancelled`,
`turn_rejected`, and `turn_event`. For `turn_event`, the SSE event name is the
nested `runtime_event.type`. The outer `cursor` is the value to persist for
session reconnect; the nested `sequence` remains the cursor for that one turn.

## Wire format

```text
id: 3
event: provider_event_received
data: {"sequence":3,"session_id":"demo","turn_id":"TURN_ID","type":"provider_event_received","provider_event":{"type":"content_received","content":"Hello"}}
```

- `id` is the per-turn numeric sequence.
- `event` equals the runtime event type.
- `data` is one stable JSON event envelope.
- `: keepalive` comments are emitted every 15 seconds by default.

## Reconnect

Persist the last fully rendered event ID and send it back:

```bash
curl -NsS -H 'Last-Event-ID: 3' \
  http://127.0.0.1:8080/v1/sessions/demo/turns/TURN_ID/events
```

Or use `?after=3`. The server subscribes to live events before reading retained
events through the subscription fence, preventing a replay/live gap. Events up
to and including sequence 3 are omitted.

Never reuse a cursor for a different session or turn. Invalid cursor text or a
cursor whose identity does not match the run returns `400 invalid_cursor`.

## Event envelope

Every payload has:

```json
{
  "sequence": 3,
  "session_id": "demo",
  "turn_id": "TURN_ID",
  "type": "provider_event_received"
}
```

Depending on `type`, it additionally contains exactly relevant fields:

| Field | Used by |
| --- | --- |
| `provider_event` | Provider content, reasoning, tool fragments, completion, errors. |
| `tool_request` | `tool_call_requested`. |
| `tool_result` | `tool_result_received`. |
| `result` | `run_completed`. |
| `error` | `run_failed` and nested provider errors. |
| `reason` | Interruption/cancellation context. |
| `permission` | Permission request/cancel/expiry events. |
| `decision` | `permission_resolved`. |
| `permission_mode` | Run-start mode and mode changes. |
| `confirmation` | Confirmation request/cancel/expiry events. |
| `confirmation_decision` | `confirmation_resolved`. |

## Runtime event catalog

`run_started` is the first admitted-turn event. Exactly one of
`run_completed`, `run_failed`, or `agent_interrupted` terminates the stream.
Queued turns emit nothing until admitted.

| Event type | Additional payload | Meaning |
| --- | --- | --- |
| `run_started` | `message`, `permission_mode` | The turn was admitted and its user message was stored. `permission_mode.current` is the mode captured for the run. |
| `provider_event_received` | `provider_event` | One provider-neutral streaming fragment was received. See the provider event catalog below. |
| `tool_call_requested` | `tool_request` | A complete provider tool call was assembled, validated, and submitted for permission/confirmation/execution. |
| `tool_result_received` | `tool_result` | Tool execution reached a terminal `succeeded`, `failed`, `interrupted`, `denied`, or `declined` result. |
| `permission_requested` | `permission` | Tool execution is paused until a correlated permission decision arrives or the request expires. |
| `permission_resolved` | `permission`, `decision` | A permission decision was accepted. This event is committed before its resulting tool result. |
| `permission_cancelled` | `permission` | A pending permission was cancelled because its run or runtime stopped. |
| `permission_expired` | `permission` | No decision arrived before `expires_at`; the tool does not execute. |
| `confirmation_requested` | `confirmation` | A custom tool is paused while the user reviews its information and answers Yes or No. |
| `confirmation_resolved` | `confirmation`, `confirmation_decision` | A confirmation answer was accepted. This event is committed before its resulting tool result. |
| `confirmation_cancelled` | `confirmation` | A pending confirmation was cancelled because its run or runtime stopped. |
| `confirmation_expired` | `confirmation` | No answer arrived before `expires_at`; the tool does not execute. |
| `permission_mode_changed` | `permission_mode` | Live policy changed; `previous` and `current` identify the transition. Existing in-flight decisions remain correlated to their request. |
| `run_completed` | `result` | Terminal success. `result` contains final text, reasoning, tool results, provider-step count, and completion state. |
| `run_failed` | `error` | Terminal failure, such as a provider error, closed executor, invalid tool result, or maximum-step exhaustion. |
| `agent_interrupted` | `reason` | Terminal cancellation requested by a caller, server shutdown, or context cancellation. |

## Provider event payload

```json
{
  "provider_event": {
    "type": "content_received",
    "content": "partial text",
    "reasoning": "",
    "finish_reason": ""
  }
}
```

Tool-stream fragments may include:

```json
{
  "provider_event": {
    "type": "tool_call_started",
    "tool": {
      "index": 0,
      "id": "call_123",
      "type": "function",
      "name": "lookup_topic",
      "arguments": "{\"topic\":\"Go\"}"
    }
  }
}
```

Do not execute provider fragments directly. The runtime assembles a complete
validated call and emits `tool_call_requested`.

### Provider event catalog

| `provider_event.type` | Fields | Meaning |
| --- | --- | --- |
| `content_received` | `content` | Incremental assistant-visible text. Append fragments in arrival order. |
| `reasoning_received` | `reasoning` | Incremental reasoning text when the provider exposes it. Keep it separate from assistant content. |
| `tool_call_started` | `tool.index`, `tool.id`, `tool.name`, `tool.type` | Begins one streamed tool call. `index` correlates later argument fragments. |
| `tool_arguments_received` | `tool.index`, `tool.arguments` | Adds an argument-string fragment; it may not be valid JSON until completion. |
| `tool_call_completed` | `tool.index` and completed tool metadata | Signals that the provider finished the streamed call. Wait for `tool_call_requested` before execution. |
| `stream_completed` | `finish_reason` | The provider step ended normally. The runtime may still execute tools and start another provider step. |
| `stream_failed` | `error` | The provider stream failed. A terminal `run_failed` follows unless the runtime has already terminated. |

## Permission event

```json
{
  "type": "permission_requested",
  "permission": {
    "id": "perm_...",
    "session_id": "demo",
    "turn_id": "TURN_ID",
    "call_id": "CALL_ID",
    "tool_name": "publish_report",
    "details": "Destination: production",
    "reason": "Uploads a report",
    "risk": "high",
    "actions": ["network.access"],
    "created_at": "2026-07-21T00:00:00Z"
  }
}
```

Keep rendering the request after an SSE disconnect. Decision state belongs to
the agent and storage, not to one HTTP connection.

## Confirmation event

```json
{
  "type": "confirmation_requested",
  "confirmation": {
    "id": "confirm_...",
    "session_id": "demo",
    "turn_id": "TURN_ID",
    "call_id": "CALL_ID",
    "tool_name": "publish_report",
    "title": "Publish report",
    "message": "Publish this report now?",
    "details": "Destination: production",
    "created_at": "2026-07-21T00:00:00Z"
  }
}
```

After answering, wait for `confirmation_resolved` and then the tool result. The
runtime guarantees the resolution event precedes the result event.

## Browser example

```js
const source = new EventSource(eventsUrl);

source.addEventListener('provider_event_received', (message) => {
  const event = JSON.parse(message.data);
  if (event.provider_event?.type === 'content_received') {
    appendText(event.provider_event.content);
  }
  saveCursor(event.session_id, event.turn_id, event.sequence);
});

source.addEventListener('run_completed', (message) => {
  const event = JSON.parse(message.data);
  renderComplete(event.result);
  source.close();
});
```

Native `EventSource` reconnects with `Last-Event-ID`. When using `fetch` streams,
persist and apply the cursor explicitly.
