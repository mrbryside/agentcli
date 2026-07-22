---
title: Echo server
sidebar_position: 4
---

# Echo server

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
    agentcli.WithServerAutoContinueSubagents(true),
    agentcli.WithServerMiddleware(authenticationMiddleware),
)
```

Server options are separate from agent options. The first middleware supplied
is the outermost Echo middleware.

`WithServerTurnQueueLimit` bounds waiting root turns per session; the active
turn is not counted. The default is 64. Other sessions never wait behind this
session's queue.

`WithServerAutoContinueSubagents` defaults to `true`. A completed child is
converted into a trusted parent callback turn and published through
`GET /v1/sessions/{sessionID}/events`, giving every client the same callback
delivery behavior. Disable
it only when the embedding application consumes and continues child callbacks
itself.

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
