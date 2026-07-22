---
title: Build an application with the API
sidebar_position: 2
---

# Build an application with the API

The HTTP API can power a web chat, desktop client, messaging bot, workflow
service, or another interactive application. It exposes the complete
`agentcli` interaction loop:

1. submit user messages as turns;
2. render provider output as it streams;
3. show tool calls and results;
4. answer permissions and Yes/No confirmations;
5. interrupt active or queued work;
6. restore the provider-neutral transcript;
7. display and continue subagents;
8. receive automatic parent responses when subagents finish.

The default server and storage are in-memory. They are suitable for one
long-lived process, local tools, prototypes, and tests. Before using multiple
replicas or requiring restart recovery, add durable storage and route one
session consistently to the process that owns its active runtime.

## Recommended client architecture

Give each user-visible conversation one stable `sessionID`. Give each submitted
message an optional unique `turn_id`, and persist every URL and cursor returned
by the server.

```text
Application UI
  ├─ POST message / decision / interrupt
  ├─ GET session transcript after reload
  └─ SSE session activity stream
       ├─ user-created turns
       ├─ queued lifecycle changes
       └─ automatic subagent callback turns
              │
              ▼
        agentcli Echo server
          ├─ AgentRuntime
          ├─ tool executor
          └─ message / permission / confirmation / subagent storage
```

For a complete application, prefer the session stream:

```text
GET /v1/sessions/{sessionID}/events
```

It has one monotonic cursor across every root turn. This is how a client learns
about parent turns created asynchronously when subagents finish. A simple
request-scoped integration can instead stream only the turn returned by its
POST:

```text
GET /v1/sessions/{sessionID}/turns/{turnID}/events
```

## API selection guide

| Need | Endpoint |
| --- | --- |
| Submit a user message | `POST /v1/sessions/{sessionID}/turns` |
| Follow all root activity | `GET /v1/sessions/{sessionID}/events` |
| Follow one known turn | `GET /v1/sessions/{sessionID}/turns/{turnID}/events` |
| Read status or final result | `GET /v1/sessions/{sessionID}/turns/{turnID}` |
| Restore the transcript | `GET /v1/sessions/{sessionID}/messages` |
| Stop active or queued work | `POST /v1/sessions/{sessionID}/turns/{turnID}/interrupt` |
| Answer a tool permission | `POST /v1/permissions/{permissionID}/decisions` |
| Answer an informational confirmation | `POST /v1/confirmations/{confirmationID}/decisions` |
| Read or change permission mode | `GET`, `PUT /v1/permission-mode` |
| Discover subagent definitions | `GET /v1/subagent-definitions` |
| Create or list children | `POST`, `GET /v1/sessions/{sessionID}/subagents` |
| Open or close one terminal child | `GET`, `DELETE /v1/sessions/{sessionID}/subagents/{subagentID}` |
| Send a child message | `POST /v1/sessions/{sessionID}/subagents/{subagentID}/turns` |
| Restore a child transcript | `GET /v1/sessions/{sessionID}/subagents/{subagentID}/messages` |

The generated [API documentation](/api-reference) contains every request and
response schema. The [Events reference](/api/sse-events) contains the complete
runtime event catalog.

## Start the server

```go
agent, err := agentcli.New(ctx,
    agentcli.WithProject(project),
    agentcli.WithCustomTool("lookup_order", "Read an order", lookupOrder),
)
if err != nil {
    return err
}
defer agent.Close()

return agent.RunServer(
    agentcli.WithServerAddress("127.0.0.1:8080"),
    agentcli.WithServerTurnQueueLimit(64),
    agentcli.WithServerMiddleware(authenticationMiddleware),
)
```

Automatic subagent callback turns are enabled by default. A host that wants to
manage child completions itself can opt out with
`agentcli.WithServerAutoContinueSubagents(false)`.

## End-to-end browser flow

This example assumes the browser reaches the API through the same origin and
uses cookie authentication. Native `EventSource` cannot attach an arbitrary
`Authorization` header; for bearer authentication, use a fetch-based SSE client
or proxy the stream through your application backend.

```js
const apiBase = "";
const sessionID = crypto.randomUUID();
let sessionCursor = Number(localStorage.getItem(`agent:${sessionID}:cursor`) || 0);
let feed;

function connectSession() {
  const url = new URL(
    `${apiBase}/v1/sessions/${encodeURIComponent(sessionID)}/events`,
    location.origin,
  );
  if (sessionCursor > 0) url.searchParams.set("after", sessionCursor);

  feed = new EventSource(url);
  const eventNames = [
    "turn_queued", "turn_admitted", "turn_cancelled", "turn_rejected",
    "run_started", "provider_event_received", "tool_call_requested",
    "tool_result_received", "permission_requested", "permission_resolved",
    "permission_cancelled", "permission_expired", "confirmation_requested",
    "confirmation_resolved", "confirmation_cancelled", "confirmation_expired",
    "permission_mode_changed", "run_completed", "run_failed", "agent_interrupted",
  ];

  for (const name of eventNames) {
    feed.addEventListener(name, (message) => {
      const activity = JSON.parse(message.data);
      handleActivity(activity);
      sessionCursor = activity.cursor;
      localStorage.setItem(`agent:${sessionID}:cursor`, String(sessionCursor));
    });
  }

  feed.onerror = () => setConnectionState("reconnecting");
}

async function sendMessage(message) {
  const response = await fetch(
    `${apiBase}/v1/sessions/${encodeURIComponent(sessionID)}/turns`,
    {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({message, turn_id: crypto.randomUUID()}),
    },
  );
  if (!response.ok) throw await apiError(response);
  const turn = await response.json();
  rememberTurn(turn);
  return turn;
}
```

Connect the session stream once when the conversation opens. It replays
retained activity after the cursor and then stays live. Starting the stream
before or after `sendMessage` is safe because the server retains events.

## Render session activity

Session stream records have their own `cursor` and one of these lifecycle
types:

| `type` | Meaning |
| --- | --- |
| `turn_queued` | Accepted behind another active turn in this session. |
| `turn_admitted` | The turn now owns the session and is starting. |
| `turn_cancelled` | Queued work was removed before execution. |
| `turn_rejected` | The server admitted the turn but runtime startup failed. |
| `turn_event` | `runtime_event` contains one ordinary AgentRuntime event. |

For `turn_event`, the SSE `event:` name is the nested runtime event type. This
lets an EventSource client use the same listeners as a per-turn stream.

```js
function handleActivity(activity) {
  if (activity.type !== "turn_event") {
    updateTurnLifecycle(activity.turn_id, activity.type, activity.queue_position);
    return;
  }

  const event = activity.runtime_event;
  switch (event.type) {
    case "provider_event_received":
      if (event.provider_event.type === "content_received") {
        appendAssistantText(event.turn_id, event.provider_event.content);
      }
      break;
    case "tool_call_requested":
      showToolRunning(event.tool_request.call);
      break;
    case "tool_result_received":
      showToolResult(event.tool_result.result);
      break;
    case "permission_requested":
      showPermission(event.permission);
      break;
    case "confirmation_requested":
      showConfirmation(event.confirmation);
      break;
    case "run_completed":
      finishTurn(event.turn_id, event.result.content);
      break;
    case "run_failed":
      failTurn(event.turn_id, event.error);
      break;
    case "agent_interrupted":
      interruptTurnView(event.turn_id, event.reason);
      break;
  }
}
```

Provider text fragments are incremental. Append them in sequence; do not
replace the entire message. Do not execute `provider_event.tool` fragments.
Only `tool_call_requested` contains the complete validated tool request, and
the server-owned executor already handles it.

## Permissions and confirmations

Permissions authorize a capability. Confirmations present tool-specific
information and ask only Yes or No. A tool may require both; permission is
resolved first.

```js
async function resolvePermission(request, decision) {
  await postJSON(`/v1/permissions/${encodeURIComponent(request.id)}/decisions`, {
    session_id: request.session_id,
    turn_id: request.turn_id,
    call_id: request.call_id,
    decision, // allow_once, allow_session, allow_project, or deny
  });
}

async function resolveConfirmation(request, answer) {
  await postJSON(`/v1/confirmations/${encodeURIComponent(request.id)}/decisions`, {
    session_id: request.session_id,
    turn_id: request.turn_id,
    call_id: request.call_id,
    answer, // yes or no
  });
}
```

Keep pending prompts keyed by their request ID. Remove them only after a
resolved, cancelled, or expired event. Never infer approval from a disconnect,
timeout in the UI, or permission mode label.

For child events, post the same decision body to the ownership-scoped child
permission or confirmation endpoint shown in the API reference.

## Interrupt and queued turns

Each session runs one root turn at a time. Extra messages are accepted into a
bounded FIFO and return `202` with `status: queued`. Other sessions remain
parallel.

```js
await postJSON(
  `/v1/sessions/${encodeURIComponent(sessionID)}` +
  `/turns/${encodeURIComponent(turnID)}/interrupt`,
  {reason: "User pressed Stop"},
);
```

The same endpoint cancels a queued turn before it reaches the model. Treat
`queue_position` as a display hint: an automatic subagent callback is
prioritized ahead of waiting user turns, so positions can change.

## Subagent behavior

A root model can start project-defined subagents through its management tools,
or an application can create one directly through the nested HTTP routes.
Children always run asynchronously.

When a child turn ends, the server automatically:

1. receives its compact `completed`, `incomplete`, or `failed` callback;
2. queues a trusted `runtime_event` turn in the parent session;
3. prioritizes that callback after the currently active turn;
4. asks the parent model to use the result or error;
5. publishes the new parent turn through the session SSE stream.

The activity has `source: "subagent_callback"` and a
`subagent_callback` reference containing the child and child-turn IDs plus its
structured summary and required next step when present. The
child answer itself is delivered privately to the parent runtime; render the
parent's resulting assistant response from normal provider events. Do not poll
`subagent_status` to discover completion.

To build a child-session view, keep its transcript and streaming state separate
from the root view. Open the selected child's `/messages`, then resume its
`current_turn_id` through the nested `/events` endpoint. The complete state
model and reconnect algorithm are in [Child views](../agentcli/child-views.md).
Closing a completed or failed, callback-consumed child keeps its history readable.

## Reload and reconnect checklist

On application reload:

1. restore the stable session ID;
2. fetch `/v1/sessions/{sessionID}/messages` for the canonical transcript;
3. restore the last fully applied session cursor;
4. reconnect `/v1/sessions/{sessionID}/events?after={cursor}`;
5. rebuild pending prompts by applying replayed requested/resolved/cancelled/
   expired events in order;
6. list subagents when the UI exposes child navigation.

Persist a cursor only after its record has been applied to UI state. Applying
the same event twice should be harmless; key turns by `turn_id`, messages by
`id`, and safety prompts by their request ID.

An SSE disconnect is not a failed agent turn. Reconnect first, then use the
turn status endpoint only when you need an explicit status snapshot.

## Service-to-service authentication

The server binds to loopback by default and does not prescribe an identity
system. Before exposing it, add Echo middleware for authentication,
authorization, rate limiting, audit logging, and CORS. Authorize every request
against the session in its path; do not treat an unguessable session ID as
authentication.

For a backend service, send an authorization header on both JSON and SSE
requests. Read SSE incrementally with an HTTP client, persist `id`, and send it
as `Last-Event-ID` after reconnecting. Do not buffer the entire response.

## Common application use cases

- **Web or mobile assistant:** one session per chat, session SSE for live text,
  modals for permissions and confirmations, transcript endpoint on reload.
- **Slack, Teams, or Discord bot:** map a thread to a session ID, translate
  completed parent turns into messages, and use interactive buttons for safety
  decisions.
- **Operations console:** render tool execution and reasoning separately,
  require confirmation for deployment tools, and audit every decision event.
- **Background research workflow:** create several subagents, keep the parent
  session stream open, and let callback turns synthesize results as children
  finish.
- **Custom interactive client:** use the session feed for the root view and
  independent nested streams for child navigation and background progress.
