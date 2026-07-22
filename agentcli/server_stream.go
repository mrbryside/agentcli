package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"harness-api/agentruntime"

	"github.com/labstack/echo/v4"
)

func (server *Server) streamServerTurn(c echo.Context, turn *serverTurn, after uint64) error {
	run, err := turn.wait(c.Request().Context())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return server.writeRuntimeError(c, err)
	}
	return server.streamRun(c, run, after)
}

func (server *Server) streamRun(c echo.Context, run *agentruntime.Run, after uint64) error {
	response := c.Response()
	writer := response.Writer
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return writeAPIError(c, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support streaming")
	}

	subscription := run.Subscribe(c.Request().Context())
	afterCursor := agentruntime.EventCursor{}
	if after != 0 {
		afterCursor = agentruntime.EventCursor{SessionID: run.SessionID(), TurnID: run.TurnID(), Sequence: after}
	}
	backfill, err := run.EventsBetween(afterCursor, subscription.Cursor)
	if err != nil {
		return writeAPIError(c, http.StatusBadRequest, "invalid_cursor", err.Error())
	}

	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()

	for _, event := range backfill {
		if err := writeSSE(writer, event); err != nil {
			return nil
		}
		flusher.Flush()
	}

	heartbeat := time.NewTicker(server.config.heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case <-server.context.Done():
			return nil
		case <-heartbeat.C:
			if _, err := fmt.Fprint(writer, ": keepalive\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case event, open := <-subscription.Events:
			if !open {
				return nil
			}
			if err := writeSSE(writer, event); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

func writeSSE(writer http.ResponseWriter, event agentruntime.AgentEvent) error {
	payload, err := json.Marshal(newEventResponse(event))
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, payload); err != nil {
		return err
	}
	return nil
}
