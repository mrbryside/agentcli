---
title: HTTP & SSE Server
sidebar_position: 3
---

# HTTP and SSE Server

Every `Agent` can expose JSON commands, retained state, and Server-Sent Events
through Echo.

```go
agent, err := agentcli.New(ctx, options...)
if err != nil {
    return err
}
defer agent.Close()

return agent.RunServer()
```

The default address is `127.0.0.1:8080` so the API is not exposed publicly by
accident.

## Configure or embed

```go
server, err := agentcli.NewServer(agent,
    agentcli.WithServerAddress("127.0.0.1:9090"),
    agentcli.WithServerHeartbeat(15*time.Second),
    agentcli.WithServerRequestLimit(1<<20),
    agentcli.WithServerTurnQueueLimit(32),
    agentcli.WithServerMiddleware(authenticationMiddleware),
)
if err != nil {
    return err
}

server.Echo().GET("/healthz", healthHandler)
httpServer := &http.Server{Addr: ":9090", Handler: server.Handler()}
```

The default request limit is 1 MiB and the default queue allows 64 waiting
turns per session. Different sessions continue independently. Call
`server.Shutdown(ctx)` to stop its listener and runs; the application still
owns and must close the `Agent`.

## Core endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/sessions/{sessionID}/turns` | Start or queue a turn. |
| `GET` | `/v1/sessions/{sessionID}/turns/{turnID}` | Read turn status/result. |
| `GET` | `/v1/sessions/{sessionID}/turns/{turnID}/events` | Replay and follow one turn. |
| `GET` | `/v1/sessions/{sessionID}/events` | Replay and follow the whole session. |
| `GET` | `/v1/sessions/{sessionID}/messages` | Read the transcript. |
| `POST` | `/v1/sessions/{sessionID}/turns/{turnID}/interrupt` | Interrupt active or queued work. |
| `GET` / `PUT` | `/v1/permission-mode` | Read or change the live mode. |

Host-side task-session, permission, and confirmation routes are also
available. Use the generated [HTTP API reference](/api-reference) for every
endpoint and schema.

Start a turn:

```sh
curl -sS -X POST http://127.0.0.1:8080/v1/sessions/demo/turns \
  -H 'Content-Type: application/json' \
  -d '{"message":"Explain this project"}'
```

An idle session returns `201` with an active turn. A busy session accepts the
request into its FIFO and returns `202` with `status: "queued"` and a
`queue_position`.

## SSE and reconnects

Use the session stream for an application UI and the turn stream for a focused
run view:

```sh
curl -NsS http://127.0.0.1:8080/v1/sessions/demo/events
```

SSE `id` values are cursors. Persist the last fully rendered ID and reconnect
with `Last-Event-ID` or `?after=`. Cursors belong to one session or turn and
must not be reused for another stream.

```text
id: 3
event: provider_event_received
data: {"sequence":3,"session_id":"demo","turn_id":"turn_...","type":"provider_event_received",...}
```

The server subscribes before replaying retained events, preventing a gap
between history and live activity. Session replay is process-local; durable
recovery after restart comes from message and task-session storage.

The main session stream includes queued-turn lifecycle, normal runtime events,
scope boundaries, task completion, task-session safety decisions, and task
closure. Hydrate both messages and task sessions after a full page refresh;
system activity is not stored as conversation messages.

## Production checklist

Before binding outside loopback, add authentication/authorization, TLS, CORS,
rate limits, decision audit logs, and durable storage appropriate to the
deployment. Declared permissions are policy checks, not OS containment.
