---
title: Echo server
sidebar_position: 4
---

# Echo server

## Task delivery (v0.1)

Task completion is Agent-owned. A foreground `task` returns child output in
the same main turn. Background work and `WithTaskForegroundWait` promotion
return `running`; Agent injects one trusted result at a safe boundary or starts
one continuation. The server and its clients do not configure or operate a
subagent-result pump. `WithServerAutoContinueSubagents` was removed and is not
a current integration option.

Nested `/subagents` routes remain host-only session management. Session SSE
publishes each terminal background/promoted result exactly once as
`task_completed` (source `task`) with task/session/turn/agent/state and any
application-only result-contract metadata.

Every `Agent` can expose the complete JSON/SSE API without a separate server
package.

## Default server

```go
agent, err := agentcli.New(ctx, options...)
if err != nil {
    return err
}
defer agent.Close()

return agent.RunServer()
```

The default address is `127.0.0.1:8080`; it is intentionally not exposed on all
network interfaces.

## Configure the server

```go
return agent.RunServer(
    agentcli.WithServerAddress("127.0.0.1:9090"),
    agentcli.WithServerHeartbeat(15*time.Second),
    agentcli.WithServerRequestLimit(1<<20),
    agentcli.WithServerTurnQueueLimit(32),
    agentcli.WithServerMiddleware(authenticationMiddleware),
)
```

Server options are separate from agent options. The first middleware supplied
is the outermost Echo middleware.

`WithServerTurnQueueLimit` bounds waiting main-agent turns per session; the active
turn is not counted. The default is 64. Other sessions never wait behind this
session's queue.

Agent owns all background or promoted task delivery. A terminal result is
injected at a safe provider boundary or handled by one Agent-created
continuation; server configuration cannot disable or replace that behavior.

## Embed in an existing process

```go
server, err := agentcli.NewServer(agent,
    agentcli.WithServerAddress("127.0.0.1:9090"),
)
if err != nil {
    return err
}

server.Echo().GET("/healthz", func(c echo.Context) error {
    return c.JSON(200, map[string]string{"status": "ok"})
})

httpServer := &http.Server{
    Addr:    ":9090",
    Handler: server.Handler(),
}
```

`Server.Echo()` exposes the underlying Echo instance. Add application routes
and middleware before serving.

## Shutdown ownership

```go
if err := server.Shutdown(ctx); err != nil {
    return err
}
```

Shutdown stops the listener, cancels active runs created through that server,
and rejects its queued turns. It does not close the `Agent`; the application
owns the agent lifecycle.

## Production requirements

The framework does not guess your deployment trust model. Before binding to a
non-loopback address, add:

- authentication and authorization;
- TLS at Echo or a trusted reverse proxy;
- CORS policy for browser clients;
- request rate limits and concurrent-turn limits;
- audit logging for permission and confirmation decisions;
- durable storage if state must survive process restarts.

See the [HTTP API](../api/http-api.md) and [SSE protocol](../api/sse-events.md).
