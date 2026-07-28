---
title: HTTP API
sidebar_position: 1
---

# HTTP API

## Tasks and child-session routes

The main model's `task` tool is not an HTTP endpoint. `/subagents` endpoints
remain host-only persisted-session management. Follow session SSE for
`task_completed` (source `task`) when a background or auto-promoted task ends;
the event has task ID, child session/turn, agent, state, and trusted
application-only metadata. No client-side injection or continuation endpoint
exists in v0.1.

The Echo server exposes JSON commands, retained state, and SSE streams. The
default base URL is `http://127.0.0.1:8080`.

For the generated endpoint and schema reference, open the
[Redocly API documentation](/api-reference). Its Swagger description is
generated from the Go handlers with Swaggo during every documentation build.

Regenerate and validate it directly from `documentation/`:

```bash
npm run api:generate
npm run api:lint
npm run api:render
```

`api:generate` runs the pinned Swaggo CLI and writes `swagger.json` and
`swagger.yaml` under `documentation/static/openapi/`. `api:render` uses the
pinned Redocly CLI to create the standalone Redoc page. `npm run start` and
`npm run build` run the complete `api:docs` pipeline automatically. On every
push to `main`, the GitHub Pages workflow runs the same generation, lint, and
render steps, then rejects the build if the tracked Swagger or Redoc artifacts
differ before building Docusaurus.

All request bodies use `application/json`, reject unknown fields, accept exactly
one JSON value, and default to a 1 MiB limit.

## Endpoint summary

### Main-agent sessions

| Method | Path | Result |
| --- | --- | --- |
| `GET` | `/healthz` | Health status. |
| `POST` | `/v1/sessions/{sessionID}/turns` | Start a main-agent turn. |
| `GET` | `/v1/sessions/{sessionID}/events` | Retained/live activity across all main-agent turns. |
| `GET` | `/v1/sessions/{sessionID}/turns/{turnID}` | Read run status/result. |
| `GET` | `/v1/sessions/{sessionID}/turns/{turnID}/events` | Retained plus live SSE. |
| `POST` | `/v1/sessions/{sessionID}/turns/{turnID}/interrupt` | Interrupt one turn. |
| `GET` | `/v1/sessions/{sessionID}/messages` | Read generic transcript. |

### Decisions and policy

| Method | Path | Result |
| --- | --- | --- |
| `POST` | `/v1/permissions/{permissionID}/decisions` | Resolve a main-agent permission. |
| `POST` | `/v1/confirmations/{confirmationID}/decisions` | Resolve a main-agent confirmation. |
| `GET` | `/v1/permission-mode` | Read current permission mode. |
| `PUT` | `/v1/permission-mode` | Change permission mode. |

### Subagents

| Method | Path |
| --- | --- |
| `GET` | `/v1/subagent-definitions` |
| `POST`, `GET` | `/v1/sessions/{mainAgentSessionID}/subagents` |
| `GET`, `DELETE` | `/v1/sessions/{mainAgentSessionID}/subagents/{subagentID}` |
| `POST` | `/v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns` |
| `GET` | `/v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/messages` |
| `GET` | `/v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns/{turnID}` |
| `GET` | `/v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns/{turnID}/events` |
| `POST` | `/v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns/{turnID}/interrupt` |
| `POST` | `/v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/permissions/{permissionID}/decisions` |
| `POST` | `/v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/confirmations/{confirmationID}/decisions` |
| `GET` | `/v1/sessions/{mainAgentSessionID}/subagent-permissions` |
| `GET` | `/v1/sessions/{mainAgentSessionID}/subagent-confirmations` |

Every nested route verifies main agent ownership; a subagent ID alone is insufficient.

## Start a turn

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/sessions/demo/turns \
  -H 'Content-Type: application/json' \
  -d '{"message":"Explain the project event model"}'
```

Optional caller-defined turn ID:

```json
{
  "message": "Explain the project event model",
  "turn_id": "request-018f"
}
```

When the session is idle, the response is `201 Created`:

```json
{
  "session_id": "demo",
  "turn_id": "turn_2e23...",
  "status": "active",
  "turn_url": "/v1/sessions/demo/turns/turn_2e23...",
  "events_url": "/v1/sessions/demo/turns/turn_2e23.../events",
  "session_events_url": "/v1/sessions/demo/events",
  "messages_url": "/v1/sessions/demo/messages"
}
```

When another turn already owns the session, the request is accepted rather
than rejected. The response is `202 Accepted`:

```json
{
  "session_id": "demo",
  "turn_id": "turn_queued...",
  "status": "queued",
  "queue_position": 1,
  "turn_url": "/v1/sessions/demo/turns/turn_queued...",
  "events_url": "/v1/sessions/demo/turns/turn_queued.../events",
  "session_events_url": "/v1/sessions/demo/events",
  "messages_url": "/v1/sessions/demo/messages"
}
```

Queued turns execute FIFO. The default maximum is 64 queued turns per session;
overflow returns `429 turn_queue_full`. A different session starts immediately
and runs in parallel.

For a complete chat application, keep `session_events_url` open. Its cursor is
monotonic across user turns and server-created subagent result turns. See
[Build an application with the API](../examples/api-client-integration.md).

The response also sets `Location`, `X-Session-ID`, and `X-Turn-ID`. If the POST
includes `Accept: text/event-stream`, it streams events in the same response
instead of returning JSON. For a queued turn, the connection waits until that
turn is admitted, then returns the ordinary SSE stream.

## Read or interrupt a turn

```bash
curl -sS http://127.0.0.1:8080/v1/sessions/demo/turns/TURN_ID
```

Completed response:

```json
{
  "session_id": "demo",
  "turn_id": "TURN_ID",
  "status": "done",
  "result": {
    "session_id": "demo",
    "turn_id": "TURN_ID",
    "content": "Final assistant text",
    "tool_results": [],
    "steps": 1,
    "finished": true
  }
}
```

Queued response:

```json
{
  "session_id": "demo",
  "turn_id": "turn_queued...",
  "status": "queued",
  "queue_position": 1
}
```

Interrupt:

```bash
curl -sS -X POST \
  http://127.0.0.1:8080/v1/sessions/demo/turns/TURN_ID/interrupt \
  -H 'Content-Type: application/json' \
  -d '{"reason":"user cancelled"}'
```

An empty request body is also valid. Success is `202 Accepted`. For a queued
turn, interruption removes it before the model runs and returns
`{"status":"queued_turn_cancelled"}`. Its subsequent status is `done` with a
terminal cancellation error.

## Conversation messages

```bash
curl -sS http://127.0.0.1:8080/v1/sessions/demo/messages
```

Each message contains `id`, `session_id`, `turn_id`, `type`, and `created_at`,
plus one of text content, tool calls, or a tool result. Types include `user`,
`runtime_event`, `assistant`, `tool_call`, and `tool_result`.

Tool results normally omit `trigger_satisfied`. A successful
`EndResponseScope` result sets it explicitly: `false` means the model called
the tool before the final response-scope boundary, so the runtime skipped the
handler; `true` means the final handler executed and satisfied the required
trigger. The same field appears at `tool_result.result.trigger_satisfied` in
SSE events and in each terminal `result.tool_results` entry.

## Resolve permission

Use IDs from the `permission_requested` SSE event:

```bash
curl -sS -X POST \
  http://127.0.0.1:8080/v1/permissions/PERMISSION_ID/decisions \
  -H 'Content-Type: application/json' \
  -d '{
    "session_id":"demo",
    "turn_id":"TURN_ID",
    "call_id":"CALL_ID",
    "decision":"allow_once"
  }'
```

Valid decisions are `allow_once`, `allow_session`, `allow_project`, and `deny`.

## Resolve confirmation

```bash
curl -sS -X POST \
  http://127.0.0.1:8080/v1/confirmations/CONFIRMATION_ID/decisions \
  -H 'Content-Type: application/json' \
  -d '{
    "session_id":"demo",
    "turn_id":"TURN_ID",
    "call_id":"CALL_ID",
    "answer":"yes"
  }'
```

Valid answers are `yes` and `no`.

## Permission mode

```bash
curl -sS http://127.0.0.1:8080/v1/permission-mode

curl -sS -X PUT http://127.0.0.1:8080/v1/permission-mode \
  -H 'Content-Type: application/json' \
  -d '{"mode":"criticalOnly"}'
```

## Create and continue a subagent

```bash
curl -sS -X POST \
  http://127.0.0.1:8080/v1/sessions/demo/subagents \
  -H 'Content-Type: application/json' \
  -d '{
    "name":"researcher",
    "message":"Compare the storage implementations",
    "label":"storage research",
    "main_agent_turn_id":"TURN_ID"
  }'
```

`main_agent_turn_id` is optional for UI-created subagents; the server generates a
synthetic main agent turn identity when omitted. Creation returns `201 Created` and
starts the subagent asynchronously.

Subagent JSON uses explicit identities throughout:

```json
{
  "id": "subagent_123",
  "display_name": "Vale",
  "label": "storage research",
  "main_agent_session_id": "demo",
  "main_agent_turn_id": "TURN_ID",
  "subagent_session_id": "session_subagent",
  "definition_name": "researcher",
  "provider": "primary",
  "model": "gpt-4.1-mini",
  "status": "running",
  "current_subagent_turn_id": "turn_subagent",
  "version": 1,
  "queued_messages": 0
}
```

Completed records use `last_subagent_turn_id`, `last_result_status`,
`last_result_summary`, `last_result_next_step`, and `last_result_error` when
those values exist. The API does not emit legacy identity or result aliases.

By default, subagent completion first tries to append trusted result input at
the next provider boundary of a compatible active main agent. Its subsequent
provider events stay on that active turn's stream. If no compatible run
remains, the server creates a result turn with
`source: subagent_result`. Clients do not need to poll or manually ask the
main agent to read the subagent.

When the subagent originated from a main-agent assignment, its Agent-owned
result remains in that original response scope. A subagent created directly
through this HTTP endpoint has no originating scope, so its result
continuation starts a new main-agent response scope instead.

Continue an existing subagent:

```bash
curl -sS -X POST \
  http://127.0.0.1:8080/v1/sessions/demo/subagents/SUBAGENT_ID/turns \
  -H 'Content-Type: application/json' \
  -d '{"message":"Now focus on confirmation storage"}'
```

This returns `202 Accepted`. If the subagent was retained, `Location` identifies its
new turn. If it was active, the message is queued and no turn exists for that
queued input yet. Supplying `Accept: text/event-stream` streams only an
immediately started turn, not queued mailbox work.

List subagents, including closed records:

```bash
curl -sS 'http://127.0.0.1:8080/v1/sessions/demo/subagents?include_closed=true'
```

Subagent permission and confirmation bodies use the same fields as main-agent
decisions. Their nested endpoints force ownership and derive the subagent session
from the owned record. On reload, query `subagent-permissions` and
`subagent-confirmations` to recover unresolved subagent gates that may predate the
current session-stream subscription.

Sending to a running subagent queues the message. Sending to any retained
completed, incomplete, or failed task starts a new child turn with the same
task ID and transcript. A closed record returns `409 task_closed`.

Delete uses the same destructive lifecycle path as `Agent.CloseSubagent`. It
may interrupt active work, drops queued input, retains transcript and completed
event history, and cancels outstanding unreserved result obligations to
release the main agent response scope's result barrier. Bind it to an explicit
application or user action. Cancellation remains terminal across racing
assignment registration and result-reservation rollback. The endpoint changes
lifecycle and scope accounting; it does not create a provider turn or
user-visible response. See
[Subagent lifecycle control](../capabilities/subagent-lifecycle-control.md).

## Errors

Errors have a stable envelope:

```json
{
  "error": {
    "code": "invalid_request",
    "message": "message is required"
  }
}
```

Common mappings:

| HTTP | Code | Examples |
| --- | --- | --- |
| `400` | `invalid_request`, `invalid_json`, `invalid_cursor` | Missing identity, invalid decision, malformed JSON. |
| `404` | `run_not_found`, `not_found` | Unknown run, permission, confirmation, or subagent. |
| `409` | `conflict`, `turn_cancelled` | Reused turn ID, cancelled queued stream, already resolved decision, send before result consumption, or invalid subagent cleanup state. |
| `429` | `turn_queue_full` | Per-session waiting-turn bound reached. |
| `408` | `request_cancelled` | Context cancellation or deadline. |
| `413` | `request_too_large` | JSON body exceeds configured limit. |
| `503` | `closed` | Agent/storage/decision channel is closed. |
| `500` | `internal_error` | Unexpected infrastructure failure. |
