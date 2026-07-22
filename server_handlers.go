package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage"

	"github.com/labstack/echo/v4"
)

func (server *Server) routes() {
	server.echo.GET("/healthz", server.health)
	server.subagentRoutes()
	server.echo.POST("/v1/sessions/:sessionID/turns", server.startTurn)
	server.echo.GET("/v1/sessions/:sessionID/events", server.streamSessionEvents)
	server.echo.GET("/v1/sessions/:sessionID/turns/:turnID", server.getTurn)
	server.echo.GET("/v1/sessions/:sessionID/turns/:turnID/events", server.streamTurn)
	server.echo.POST("/v1/sessions/:sessionID/turns/:turnID/interrupt", server.interruptTurn)
	server.echo.GET("/v1/sessions/:sessionID/messages", server.listMessages)
	server.echo.POST("/v1/permissions/:permissionID/decisions", server.resolvePermission)
	server.echo.POST("/v1/confirmations/:confirmationID/decisions", server.resolveConfirmation)
	server.echo.GET("/v1/permission-mode", server.getPermissionMode)
	server.echo.PUT("/v1/permission-mode", server.setPermissionMode)
}

// health godoc
// @Summary Check server health
// @ID health
// @Description Returns successfully when the agent HTTP server is available.
// @Tags System
// @Produce json
// @Success 200 {object} map[string]string
// @Router /healthz [get]
func (server *Server) health(c echo.Context) error {
	return writeJSON(c, http.StatusOK, map[string]string{"status": "ok"})
}

// startTurn godoc
// @Summary Start or queue a root agent turn
// @ID startRootTurn
// @Description Starts immediately when the session is idle or queues FIFO when another turn is active. Send Accept: text/event-stream to stream an admitted turn instead of receiving JSON.
// @Tags Turns
// @Accept json
// @Produce json
// @Produce text/event-stream
// @Param sessionID path string true "Session ID"
// @Param request body StartTurnRequest true "Turn input"
// @Success 201 {object} StartTurnResponse
// @Success 202 {object} StartTurnResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Failure 429 {object} APIErrorResponse
// @Failure 500 {object} APIErrorResponse
// @Router /v1/sessions/{sessionID}/turns [post]
func (server *Server) startTurn(c echo.Context) error {
	sessionID := c.Param("sessionID")
	if sessionID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "session ID is required")
	}
	var body StartTurnRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	if strings.TrimSpace(body.Message) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "message is required")
	}
	turn, queued, err := server.submitTurn(c.Request().Context(), agentruntime.Request{
		SessionID: sessionID,
		TurnID:    body.TurnID,
		Message: agentruntime.Message{
			Type:    agentruntime.MessageTypeUser,
			Content: body.Message,
		},
	})
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	turnID := turn.request.TurnID
	c.Response().Header().Set("Location", turnPath(sessionID, turnID))
	c.Response().Header().Set("X-Session-ID", sessionID)
	c.Response().Header().Set("X-Turn-ID", turnID)

	if acceptsEventStream(c.Request()) {
		return server.streamServerTurn(c, turn, 0)
	}
	status := RunStatusQueued
	statusCode := http.StatusAccepted
	queuePosition := server.queuePosition(turn)
	if !queued {
		statusCode = http.StatusCreated
		if run, _ := turn.snapshot(); run != nil {
			status = run.Status()
		}
	}
	return writeJSON(c, statusCode, StartTurnResponse{
		SessionID:        sessionID,
		TurnID:           turnID,
		Status:           status,
		QueuePosition:    queuePosition,
		TurnURL:          turnPath(sessionID, turnID),
		EventsURL:        turnPath(sessionID, turnID) + "/events",
		SessionEventsURL: "/v1/sessions/" + url.PathEscape(sessionID) + "/events",
		MessagesURL:      "/v1/sessions/" + url.PathEscape(sessionID) + "/messages",
	})
}

// getTurn godoc
// @Summary Read root turn status and result
// @ID getRootTurn
// @Tags Turns
// @Produce json
// @Param sessionID path string true "Session ID"
// @Param turnID path string true "Turn ID"
// @Success 200 {object} TurnResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{sessionID}/turns/{turnID} [get]
func (server *Server) getTurn(c echo.Context) error {
	turn, found := server.findTurn(c.Param("sessionID"), c.Param("turnID"))
	if !found {
		return writeAPIError(c, http.StatusNotFound, "run_not_found", "run was not found")
	}
	run, turnErr := turn.snapshot()
	if run == nil {
		response := TurnResponse{SessionID: turn.request.SessionID, TurnID: turn.request.TurnID, Status: RunStatusQueued, QueuePosition: server.queuePosition(turn)}
		if turnErr != nil {
			response.Status = agentruntime.RunStatusDone
			response.Error = turnErr.Error()
		}
		return writeJSON(c, http.StatusOK, response)
	}
	response := TurnResponse{SessionID: run.SessionID(), TurnID: run.TurnID(), Status: run.Status()}
	if run.Done() {
		result, err := run.Result()
		if err != nil {
			response.Error = err.Error()
		} else {
			value := newRunResultResponse(result)
			response.Result = &value
		}
	}
	return writeJSON(c, http.StatusOK, response)
}

// streamTurn godoc
// @Summary Stream retained and live root turn events
// @ID streamRootTurnEvents
// @Description Replays events after the supplied cursor, then continues with live events. The SSE event ID is the event sequence.
// @Tags Event streams
// @Produce json
// @Produce text/event-stream
// @Param sessionID path string true "Session ID"
// @Param turnID path string true "Turn ID"
// @Param after query integer false "Resume after this event sequence"
// @Param Last-Event-ID header integer false "Resume after this event sequence when after is omitted"
// @Success 200 {object} EventResponse "One SSE data payload; the HTTP response remains open for more events"
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{sessionID}/turns/{turnID}/events [get]
func (server *Server) streamTurn(c echo.Context) error {
	turn, found := server.findTurn(c.Param("sessionID"), c.Param("turnID"))
	if !found {
		return writeAPIError(c, http.StatusNotFound, "run_not_found", "run was not found")
	}
	after, err := parseAfterSequence(c.Request())
	if err != nil {
		return writeAPIError(c, http.StatusBadRequest, "invalid_cursor", err.Error())
	}
	return server.streamServerTurn(c, turn, after)
}

// interruptTurn godoc
// @Summary Interrupt a root turn
// @ID interruptRootTurn
// @Description Cancels a queued turn before admission or requests interruption of an active turn. The body is optional.
// @Tags Turns
// @Accept json
// @Produce json
// @Param sessionID path string true "Session ID"
// @Param turnID path string true "Turn ID"
// @Param request body InterruptRequest false "Optional interruption reason"
// @Success 202 {object} map[string]string
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/sessions/{sessionID}/turns/{turnID}/interrupt [post]
func (server *Server) interruptTurn(c echo.Context) error {
	turn, found := server.findTurn(c.Param("sessionID"), c.Param("turnID"))
	if !found {
		return writeAPIError(c, http.StatusNotFound, "run_not_found", "run was not found")
	}
	var body InterruptRequest
	if c.Request().Body != nil && c.Request().ContentLength != 0 {
		if err := server.decodeJSON(c, &body); err != nil {
			return server.writeDecodeError(c, err)
		}
	}
	if server.cancelQueuedTurn(turn, body.Reason) {
		return writeJSON(c, http.StatusAccepted, map[string]string{"status": "queued_turn_cancelled"})
	}
	run, turnErr := turn.snapshot()
	if turnErr != nil {
		return server.writeRuntimeError(c, turnErr)
	}
	if run == nil {
		return writeAPIError(c, http.StatusConflict, "conflict", "turn is transitioning from queued to active")
	}
	if err := run.Interrupt(c.Request().Context(), body.Reason); err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusAccepted, map[string]string{"status": "interrupt_requested"})
}

// listMessages godoc
// @Summary List retained session messages
// @ID listSessionMessages
// @Description Returns the provider-neutral transcript, including user, assistant, tool-call, and tool-result messages.
// @Tags Messages
// @Produce json
// @Param sessionID path string true "Session ID"
// @Success 200 {object} MessagesResponse
// @Failure 500 {object} APIErrorResponse
// @Router /v1/sessions/{sessionID}/messages [get]
func (server *Server) listMessages(c echo.Context) error {
	messages, err := server.agent.ListMessages(c.Request().Context(), c.Param("sessionID"))
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	response := MessagesResponse{Messages: make([]MessageResponse, len(messages))}
	for index, message := range messages {
		response.Messages[index] = newMessageResponse(message)
	}
	return writeJSON(c, http.StatusOK, response)
}

// resolvePermission godoc
// @Summary Resolve a root tool permission
// @ID resolveRootPermission
// @Tags Permissions
// @Accept json
// @Produce json
// @Param permissionID path string true "Permission request ID"
// @Param request body PermissionDecisionRequest true "Permission decision"
// @Success 200 {object} PermissionDecisionResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/permissions/{permissionID}/decisions [post]
func (server *Server) resolvePermission(c echo.Context) error {
	permissionID := permission.ID(c.Param("permissionID"))
	if permissionID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "permission ID is required")
	}
	var body PermissionDecisionRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	decision := permission.Decision{
		PermissionID: permissionID,
		SessionID:    body.SessionID,
		TurnID:       body.TurnID,
		CallID:       body.CallID,
		Type:         body.Decision,
	}
	if err := server.agent.ResolvePermission(c.Request().Context(), decision); err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, PermissionDecisionResponse{Decision: newDecisionResponse(decision)})
}

// resolveConfirmation godoc
// @Summary Resolve a root tool confirmation
// @ID resolveRootConfirmation
// @Description Answers a tool-authored informational Yes/No confirmation. This is independent of permission policy.
// @Tags Confirmations
// @Accept json
// @Produce json
// @Param confirmationID path string true "Confirmation request ID"
// @Param request body ConfirmationDecisionRequest true "Confirmation answer"
// @Success 200 {object} ConfirmationDecisionResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/confirmations/{confirmationID}/decisions [post]
func (server *Server) resolveConfirmation(c echo.Context) error {
	confirmationID := confirmation.ID(c.Param("confirmationID"))
	if confirmationID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "confirmation ID is required")
	}
	var body ConfirmationDecisionRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	decision := confirmation.Decision{ConfirmationID: confirmationID, SessionID: body.SessionID, TurnID: body.TurnID, CallID: body.CallID, Answer: body.Answer}
	if err := server.agent.ResolveConfirmation(c.Request().Context(), decision); err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, ConfirmationDecisionResponse{Decision: newConfirmationDecisionResponse(decision)})
}

// getPermissionMode godoc
// @Summary Read the current permission mode
// @ID getPermissionMode
// @Tags Permissions
// @Produce json
// @Success 200 {object} PermissionModeResponse
// @Router /v1/permission-mode [get]
func (server *Server) getPermissionMode(c echo.Context) error {
	return writeJSON(c, http.StatusOK, PermissionModeResponse{Mode: server.agent.PermissionMode()})
}

// setPermissionMode godoc
// @Summary Change the permission mode
// @ID setPermissionMode
// @Description Changes live tool permission evaluation and emits a permission-mode event.
// @Tags Permissions
// @Accept json
// @Produce json
// @Param request body SetPermissionModeRequest true "New permission mode"
// @Success 200 {object} PermissionModeResponse
// @Failure 400 {object} APIErrorResponse
// @Router /v1/permission-mode [put]
func (server *Server) setPermissionMode(c echo.Context) error {
	var body SetPermissionModeRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	previous := server.agent.PermissionMode()
	if err := server.agent.SetPermissionMode(c.Request().Context(), body.Mode); err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, PermissionModeResponse{Previous: previous, Mode: server.agent.PermissionMode()})
}

func (server *Server) decodeJSON(c echo.Context, target any) error {
	request := c.Request()
	request.Body = http.MaxBytesReader(c.Response(), request.Body, server.config.requestLimit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func (server *Server) writeDecodeError(c echo.Context, err error) error {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return writeAPIError(c, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the configured size limit")
	}
	return writeAPIError(c, http.StatusBadRequest, "invalid_json", err.Error())
}

func (server *Server) writeRuntimeError(c echo.Context, err error) error {
	status := http.StatusInternalServerError
	code := "internal_error"
	switch {
	case errors.Is(err, agentruntime.ErrInvalidRequest), errors.Is(err, permission.ErrInvalid), errors.Is(err, confirmation.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, agentruntime.ErrRunNotFound), errors.Is(err, permission.ErrNotFound), errors.Is(err, confirmation.ErrNotFound), errors.Is(err, storage.ErrSubagentNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, agentruntime.ErrTurnInProgress), errors.Is(err, agentruntime.ErrTurnExists), errors.Is(err, permission.ErrAlreadyResolved), errors.Is(err, confirmation.ErrAlreadyResolved), errors.Is(err, storage.ErrSubagentRunning):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, errServerTurnQueueFull):
		status, code = http.StatusTooManyRequests, "turn_queue_full"
	case errors.Is(err, errQueuedTurnCancelled):
		status, code = http.StatusConflict, "turn_cancelled"
	case errors.Is(err, ErrClosed), errors.Is(err, permission.ErrClosed), errors.Is(err, confirmation.ErrClosed), errors.Is(err, storage.ErrSubagentClosed):
		status, code = http.StatusServiceUnavailable, "closed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusRequestTimeout, "request_cancelled"
	}
	return writeAPIError(c, status, code, err.Error())
}

func (server *Server) httpErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	status := http.StatusInternalServerError
	message := "internal server error"
	if httpError, ok := err.(*echo.HTTPError); ok {
		status = httpError.Code
		message = fmt.Sprint(httpError.Message)
	}
	_ = writeAPIError(c, status, "http_error", message)
}

func writeJSON(c echo.Context, status int, value any) error {
	return c.JSON(status, value)
}

func writeAPIError(c echo.Context, status int, code, message string) error {
	return writeJSON(c, status, APIErrorResponse{Error: APIError{Code: code, Message: message}})
}

func acceptsEventStream(request *http.Request) bool {
	for _, value := range strings.Split(request.Header.Get("Accept"), ",") {
		if strings.TrimSpace(strings.SplitN(value, ";", 2)[0]) == "text/event-stream" {
			return true
		}
	}
	return false
}

func parseAfterSequence(request *http.Request) (uint64, error) {
	value := request.URL.Query().Get("after")
	if value == "" {
		value = request.Header.Get("Last-Event-ID")
	}
	if value == "" {
		return 0, nil
	}
	after, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("after must be an unsigned event sequence: %w", err)
	}
	return after, nil
}

func turnPath(sessionID, turnID string) string {
	return "/v1/sessions/" + url.PathEscape(sessionID) + "/turns/" + url.PathEscape(turnID)
}
