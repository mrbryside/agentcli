package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"harness-api/agentruntime"

	"github.com/labstack/echo/v4"
)

var errSessionCursorAhead = errors.New("session event cursor is beyond retained history")

type sessionEventHub struct {
	mu       sync.Mutex
	sessions map[string]*sessionEventState
	closed   bool
}

type sessionEventState struct {
	nextSubscriber uint64
	nextCursor     uint64
	events         []SessionEventResponse
	subscribers    map[uint64]*sessionEventSubscriber
}

type sessionEventSubscriber struct {
	channel chan SessionEventResponse
	notify  chan struct{}
	queue   []SessionEventResponse
	closed  bool
}

func newSessionEventHub() *sessionEventHub {
	return &sessionEventHub{sessions: make(map[string]*sessionEventState)}
}

func (hub *sessionEventHub) stateLocked(sessionID string) *sessionEventState {
	state := hub.sessions[sessionID]
	if state == nil {
		state = &sessionEventState{subscribers: make(map[uint64]*sessionEventSubscriber)}
		hub.sessions[sessionID] = state
	}
	return state
}

func (hub *sessionEventHub) publish(event SessionEventResponse) {
	if hub == nil || event.SessionID == "" {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	state := hub.stateLocked(event.SessionID)
	state.nextCursor++
	event.Cursor = state.nextCursor
	state.events = append(state.events, event)
	for _, subscriber := range state.subscribers {
		subscriber.queue = append(subscriber.queue, event)
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func (hub *sessionEventHub) subscribe(ctx context.Context, sessionID string, after uint64) (<-chan SessionEventResponse, error) {
	ctx = nonNilContext(ctx)
	subscriber := &sessionEventSubscriber{
		channel: make(chan SessionEventResponse, 16),
		notify:  make(chan struct{}, 1),
	}
	hub.mu.Lock()
	state := hub.stateLocked(sessionID)
	if after > state.nextCursor {
		hub.mu.Unlock()
		return nil, errSessionCursorAhead
	}
	for _, event := range state.events {
		if event.Cursor > after {
			subscriber.queue = append(subscriber.queue, event)
		}
	}
	subscriber.closed = hub.closed
	state.nextSubscriber++
	id := state.nextSubscriber
	state.subscribers[id] = subscriber
	hub.mu.Unlock()

	go hub.deliver(ctx, sessionID, id, subscriber)
	return subscriber.channel, nil
}

func (hub *sessionEventHub) deliver(ctx context.Context, sessionID string, id uint64, subscriber *sessionEventSubscriber) {
	defer close(subscriber.channel)
	defer func() {
		hub.mu.Lock()
		if state := hub.sessions[sessionID]; state != nil {
			delete(state.subscribers, id)
		}
		hub.mu.Unlock()
	}()
	for {
		hub.mu.Lock()
		if len(subscriber.queue) != 0 {
			event := subscriber.queue[0]
			subscriber.queue = subscriber.queue[1:]
			hub.mu.Unlock()
			select {
			case subscriber.channel <- event:
			case <-ctx.Done():
				return
			}
			continue
		}
		closed := subscriber.closed
		hub.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-subscriber.notify:
		case <-ctx.Done():
			return
		}
	}
}

func (hub *sessionEventHub) close() {
	if hub == nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	hub.closed = true
	for _, state := range hub.sessions {
		for _, subscriber := range state.subscribers {
			subscriber.closed = true
			select {
			case subscriber.notify <- struct{}{}:
			default:
			}
		}
	}
}

// streamSessionEvents godoc
// @Summary Stream retained and live session activity
// @ID streamSessionEvents
// @Description Replays and follows every root turn in a session, including queued lifecycle records and parent turns created automatically from subagent callbacks. The session cursor is independent from each runtime event sequence.
// @Tags Event streams
// @Produce text/event-stream
// @Param sessionID path string true "Session ID"
// @Param after query integer false "Resume after this session cursor"
// @Param Last-Event-ID header integer false "Resume after this session cursor when after is omitted"
// @Success 200 {object} SessionEventResponse "One SSE data payload; the HTTP response remains open for more records"
// @Failure 400 {object} APIErrorResponse
// @Router /v1/sessions/{sessionID}/events [get]
func (server *Server) streamSessionEvents(c echo.Context) error {
	sessionID := c.Param("sessionID")
	if sessionID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "session ID is required")
	}
	after, err := parseAfterSequence(c.Request())
	if err != nil {
		return writeAPIError(c, http.StatusBadRequest, "invalid_cursor", err.Error())
	}
	events, err := server.sessionEvents.subscribe(c.Request().Context(), sessionID, after)
	if err != nil {
		return writeAPIError(c, http.StatusBadRequest, "invalid_cursor", err.Error())
	}

	response := c.Response()
	writer := response.Writer
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return writeAPIError(c, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support streaming")
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()

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
		case event, open := <-events:
			if !open {
				return nil
			}
			if err := writeSessionSSE(writer, event); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

func writeSessionSSE(writer http.ResponseWriter, event SessionEventResponse) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	eventName := string(event.Type)
	if event.Type == SessionActivityTurnEvent && event.RuntimeEvent != nil {
		eventName = string(event.RuntimeEvent.Type)
	}
	_, err = fmt.Fprintf(writer, "id: %d\nevent: %s\ndata: %s\n\n", event.Cursor, eventName, payload)
	return err
}

func newSessionRuntimeEvent(turn *serverTurn, event agentruntime.AgentEvent) SessionEventResponse {
	response := newSessionLifecycleEvent(turn, SessionActivityTurnEvent, 0, "")
	runtimeEvent := newEventResponse(event)
	response.RuntimeEvent = &runtimeEvent
	return response
}

func newSessionLifecycleEvent(turn *serverTurn, eventType SessionActivityType, queuePosition int, eventError string) SessionEventResponse {
	response := SessionEventResponse{
		Type:          eventType,
		Source:        turn.source,
		SessionID:     turn.request.SessionID,
		TurnID:        turn.request.TurnID,
		QueuePosition: queuePosition,
		TurnURL:       turnPath(turn.request.SessionID, turn.request.TurnID),
		EventsURL:     turnPath(turn.request.SessionID, turn.request.TurnID) + "/events",
		Error:         eventError,
	}
	if turn.callback != nil {
		response.SubagentCallback = &SubagentCallbackReference{
			SubagentID:     turn.callback.SubagentID,
			DisplayName:    turn.callback.DisplayName,
			DefinitionName: turn.callback.SubagentName,
			ChildSessionID: turn.callback.SessionID,
			ChildTurnID:    turn.callback.TurnID,
			Status:         turn.callback.Status,
		}
	}
	return response
}
