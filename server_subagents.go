package agentcli

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage"

	"github.com/labstack/echo/v4"
)

// subagentRoutes intentionally remain nested under their owning parent
// session. A child ID alone is never enough authority to access a transcript,
// retained run, or permission request.
func (server *Server) subagentRoutes() {
	server.echo.GET("/v1/subagent-definitions", server.listSubagentDefinitions)
	server.echo.POST("/v1/sessions/:parentSessionID/subagents", server.createSubagent)
	server.echo.GET("/v1/sessions/:parentSessionID/subagents", server.listSubagents)
	server.echo.GET("/v1/sessions/:parentSessionID/subagents/:subagentID", server.getSubagent)
	server.echo.DELETE("/v1/sessions/:parentSessionID/subagents/:subagentID", server.closeSubagent)
	server.echo.POST("/v1/sessions/:parentSessionID/subagents/:subagentID/turns", server.sendSubagentTurn)
	server.echo.GET("/v1/sessions/:parentSessionID/subagents/:subagentID/messages", server.listSubagentMessages)
	server.echo.GET("/v1/sessions/:parentSessionID/subagents/:subagentID/turns/:turnID", server.getSubagentTurn)
	server.echo.GET("/v1/sessions/:parentSessionID/subagents/:subagentID/turns/:turnID/events", server.streamSubagentTurn)
	server.echo.POST("/v1/sessions/:parentSessionID/subagents/:subagentID/turns/:turnID/interrupt", server.interruptSubagentTurn)
	server.echo.POST("/v1/sessions/:parentSessionID/subagents/:subagentID/permissions/:permissionID/decisions", server.resolveSubagentPermission)
	server.echo.POST("/v1/sessions/:parentSessionID/subagents/:subagentID/confirmations/:confirmationID/decisions", server.resolveSubagentConfirmation)
}

// listSubagentDefinitions godoc
// @Summary List available subagent definitions
// @ID listSubagentDefinitions
// @Description Returns safe discovery metadata. Local paths and private instructions are omitted.
// @Tags Subagents
// @Produce json
// @Success 200 {object} SubagentDefinitionsResponse
// @Router /v1/subagent-definitions [get]
func (server *Server) listSubagentDefinitions(c echo.Context) error {
	definitions := server.agent.SubagentDefinitions()
	response := SubagentDefinitionsResponse{Definitions: make([]SubagentDefinitionResponse, len(definitions))}
	for index, definition := range definitions {
		response.Definitions[index] = newSubagentDefinitionResponse(definition)
	}
	return writeJSON(c, http.StatusOK, response)
}

// createSubagent godoc
// @Summary Create and asynchronously start a subagent
// @ID createSubagent
// @Tags Subagents
// @Accept json
// @Produce json
// @Param parentSessionID path string true "Owning parent session ID"
// @Param request body CreateSubagentRequest true "Subagent definition and initial message"
// @Success 201 {object} SubagentResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 500 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents [post]
func (server *Server) createSubagent(c echo.Context) error {
	parentSessionID := c.Param("parentSessionID")
	if strings.TrimSpace(parentSessionID) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "parent session ID is required")
	}
	var body CreateSubagentRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Message) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "name and message are required")
	}
	parentTurnID := strings.TrimSpace(body.ParentTurnID)
	if parentTurnID == "" {
		var err error
		parentTurnID, err = newSubagentID("turn_")
		if err != nil {
			return server.writeRuntimeError(c, err)
		}
	}
	record, err := server.agent.StartSubagent(c.Request().Context(), parentSessionID, parentTurnID, body.Name, body.Message, body.Label)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	response := newSubagentResponse(record)
	c.Response().Header().Set("Location", subagentPath(parentSessionID, record.ID))
	return writeJSON(c, http.StatusCreated, response)
}

// listSubagents godoc
// @Summary List subagents owned by a parent session
// @ID listSubagents
// @Tags Subagents
// @Produce json
// @Param parentSessionID path string true "Owning parent session ID"
// @Param include_closed query boolean false "Include closed child records"
// @Success 200 {object} SubagentsResponse
// @Failure 400 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents [get]
func (server *Server) listSubagents(c echo.Context) error {
	parentSessionID := c.Param("parentSessionID")
	if strings.TrimSpace(parentSessionID) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "parent session ID is required")
	}
	includeClosed := c.QueryParam("include_closed") == "true"
	records, err := server.agent.ListSubagents(c.Request().Context(), parentSessionID, includeClosed)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, SubagentsResponse{Subagents: newSubagentResponses(records)})
}

// getSubagent godoc
// @Summary Read one owned subagent
// @ID getSubagent
// @Tags Subagents
// @Produce json
// @Param parentSessionID path string true "Owning parent session ID"
// @Param subagentID path string true "Subagent ID"
// @Success 200 {object} SubagentResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents/{subagentID} [get]
func (server *Server) getSubagent(c echo.Context) error {
	record, err := server.ownedSubagent(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, newSubagentResponse(record))
}

// closeSubagent godoc
// @Summary Close one owned subagent
// @ID closeSubagent
// @Tags Subagents
// @Produce json
// @Param parentSessionID path string true "Owning parent session ID"
// @Param subagentID path string true "Subagent ID"
// @Success 200 {object} SubagentResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents/{subagentID} [delete]
func (server *Server) closeSubagent(c echo.Context) error {
	parentSessionID, subagentID := c.Param("parentSessionID"), c.Param("subagentID")
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(subagentID) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "parent session and subagent IDs are required")
	}
	record, err := server.agent.CloseSubagent(c.Request().Context(), parentSessionID, subagentID)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, newSubagentResponse(record))
}

// sendSubagentTurn godoc
// @Summary Continue a subagent conversation
// @ID startSubagentTurn
// @Description Starts a turn when the child is idle or queues the message in its mailbox while busy. An immediately started turn can be streamed with Accept: text/event-stream.
// @Tags Subagents
// @Accept json
// @Produce json
// @Produce text/event-stream
// @Param parentSessionID path string true "Owning parent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param request body SendSubagentMessageRequest true "Next user message"
// @Success 202 {object} SubagentResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents/{subagentID}/turns [post]
func (server *Server) sendSubagentTurn(c echo.Context) error {
	parentSessionID, subagentID := c.Param("parentSessionID"), c.Param("subagentID")
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(subagentID) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "parent session and subagent IDs are required")
	}
	var body SendSubagentMessageRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	if strings.TrimSpace(body.Message) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "message is required")
	}
	record, err := server.agent.SendSubagentMessage(c.Request().Context(), parentSessionID, subagentID, body.Message)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	response := newSubagentResponse(record)
	status := http.StatusAccepted
	if record.CurrentTurnID != "" && record.Status == storage.SubagentStatusRunning {
		c.Response().Header().Set("Location", subagentTurnPath(parentSessionID, subagentID, record.CurrentTurnID))
		// A new idle child turn can be streamed from the same POST ergonomics as
		// a root turn. Queued mailbox work has no run of its own yet, so it is
		// represented by the normal accepted summary instead.
		if acceptsEventStream(c.Request()) && len(record.Pending) == 0 {
			run, runErr := server.agent.SubagentRun(c.Request().Context(), parentSessionID, subagentID, record.CurrentTurnID)
			if runErr != nil {
				return server.writeRuntimeError(c, runErr)
			}
			return server.streamRun(c, run, 0)
		}
	}
	return writeJSON(c, status, response)
}

// listSubagentMessages godoc
// @Summary List an owned subagent transcript
// @ID listSubagentMessages
// @Description Reading for UI rendering does not mark child activity as observed by the parent model.
// @Tags Subagents
// @Produce json
// @Param parentSessionID path string true "Owning parent session ID"
// @Param subagentID path string true "Subagent ID"
// @Success 200 {object} SubagentMessagesResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents/{subagentID}/messages [get]
func (server *Server) listSubagentMessages(c echo.Context) error {
	record, err := server.ownedSubagent(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	// UI reads deliberately bypass ReadSubagent: rendering a nested chat must
	// not mark activity as observed by the parent model.
	messages, err := server.agent.ListMessages(c.Request().Context(), record.SessionID)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	response := SubagentMessagesResponse{Subagent: newSubagentResponse(record), Messages: newMessageResponses(messages)}
	return writeJSON(c, http.StatusOK, response)
}

// getSubagentTurn godoc
// @Summary Read an owned subagent turn
// @ID getSubagentTurn
// @Tags Subagents
// @Produce json
// @Param parentSessionID path string true "Owning parent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param turnID path string true "Child turn ID"
// @Success 200 {object} SubagentTurnResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents/{subagentID}/turns/{turnID} [get]
func (server *Server) getSubagentTurn(c echo.Context) error {
	record, run, err := server.ownedSubagentRun(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, SubagentTurnResponse{Subagent: newSubagentResponse(record), Turn: newTurnResponse(run)})
}

// streamSubagentTurn godoc
// @Summary Stream retained and live subagent turn events
// @ID streamSubagentTurnEvents
// @Description Replays retained child-turn events after the cursor, then continues with live EventResponse envelopes.
// @Tags Event streams
// @Produce json
// @Produce text/event-stream
// @Param parentSessionID path string true "Owning parent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param turnID path string true "Child turn ID"
// @Param after query integer false "Resume after this event sequence"
// @Param Last-Event-ID header integer false "Resume after this event sequence when after is omitted"
// @Success 200 {object} EventResponse "One SSE data payload; the HTTP response remains open for more events"
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents/{subagentID}/turns/{turnID}/events [get]
func (server *Server) streamSubagentTurn(c echo.Context) error {
	_, run, err := server.ownedSubagentRun(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	after, err := parseAfterSequence(c.Request())
	if err != nil {
		return writeAPIError(c, http.StatusBadRequest, "invalid_cursor", err.Error())
	}
	return server.streamRun(c, run, after)
}

// interruptSubagentTurn godoc
// @Summary Interrupt an owned subagent turn
// @ID interruptSubagentTurn
// @Description Only the child turn currently active may be interrupted. The body is optional.
// @Tags Subagents
// @Accept json
// @Produce json
// @Param parentSessionID path string true "Owning parent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param turnID path string true "Child turn ID"
// @Param request body InterruptRequest false "Optional interruption reason"
// @Success 202 {object} SubagentTurnResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents/{subagentID}/turns/{turnID}/interrupt [post]
func (server *Server) interruptSubagentTurn(c echo.Context) error {
	parentSessionID, subagentID := c.Param("parentSessionID"), c.Param("subagentID")
	record, run, err := server.ownedSubagentRun(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	var body InterruptRequest
	if c.Request().Body != nil && c.Request().ContentLength != 0 {
		if err := server.decodeJSON(c, &body); err != nil {
			return server.writeDecodeError(c, err)
		}
	}
	// The manager interrupt operation is intentionally child-scoped because it
	// owns the active run gate. Do not let a historical turn URL interrupt a
	// newer active turn for the same child.
	if record.CurrentTurnID == run.TurnID() {
		if err := server.agent.InterruptSubagent(c.Request().Context(), parentSessionID, subagentID, body.Reason); err != nil {
			return server.writeRuntimeError(c, err)
		}
	}
	return writeJSON(c, http.StatusAccepted, SubagentTurnResponse{Subagent: newSubagentResponse(record), Turn: newTurnResponse(run)})
}

// resolveSubagentPermission godoc
// @Summary Resolve a tool permission for an owned subagent
// @ID resolveSubagentPermission
// @Tags Subagents
// @Accept json
// @Produce json
// @Param parentSessionID path string true "Owning parent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param permissionID path string true "Permission request ID"
// @Param request body PermissionDecisionRequest true "Permission decision"
// @Success 200 {object} PermissionDecisionResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents/{subagentID}/permissions/{permissionID}/decisions [post]
func (server *Server) resolveSubagentPermission(c echo.Context) error {
	parentSessionID, subagentID := c.Param("parentSessionID"), c.Param("subagentID")
	permissionID := permission.ID(c.Param("permissionID"))
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(subagentID) == "" || permissionID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "parent session, subagent, and permission IDs are required")
	}
	// Check ownership before accepting the decision; this must never route a
	// root-session decision into a child or across another parent's child.
	record, err := server.ownedSubagent(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	var body PermissionDecisionRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	decision := permission.Decision{PermissionID: permissionID, SessionID: record.SessionID, TurnID: body.TurnID, CallID: body.CallID, Type: body.Decision}
	if body.SessionID != "" && body.SessionID != record.SessionID {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "permission session does not match subagent")
	}
	if err := server.agent.ResolveSubagentPermission(c.Request().Context(), parentSessionID, subagentID, decision); err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, PermissionDecisionResponse{Decision: newDecisionResponse(decision)})
}

// resolveSubagentConfirmation godoc
// @Summary Resolve a tool confirmation for an owned subagent
// @ID resolveSubagentConfirmation
// @Tags Subagents
// @Accept json
// @Produce json
// @Param parentSessionID path string true "Owning parent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param confirmationID path string true "Confirmation request ID"
// @Param request body ConfirmationDecisionRequest true "Confirmation answer"
// @Success 200 {object} ConfirmationDecisionResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagents/{subagentID}/confirmations/{confirmationID}/decisions [post]
func (server *Server) resolveSubagentConfirmation(c echo.Context) error {
	parentSessionID, subagentID := c.Param("parentSessionID"), c.Param("subagentID")
	confirmationID := confirmation.ID(c.Param("confirmationID"))
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(subagentID) == "" || confirmationID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "parent session, subagent, and confirmation IDs are required")
	}
	record, err := server.ownedSubagent(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	var body ConfirmationDecisionRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	if body.SessionID != "" && body.SessionID != record.SessionID {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "confirmation session does not match subagent")
	}
	decision := confirmation.Decision{ConfirmationID: confirmationID, SessionID: record.SessionID, TurnID: body.TurnID, CallID: body.CallID, Answer: body.Answer}
	if err := server.agent.ResolveSubagentConfirmation(c.Request().Context(), parentSessionID, subagentID, decision); err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, ConfirmationDecisionResponse{Decision: newConfirmationDecisionResponse(decision)})
}

func (server *Server) ownedSubagent(c echo.Context) (storage.Subagent, error) {
	manager, err := server.agent.subagentManager()
	if err != nil {
		return storage.Subagent{}, err
	}
	return manager.getOwned(c.Request().Context(), c.Param("parentSessionID"), c.Param("subagentID"))
}

func (server *Server) ownedSubagentRun(c echo.Context) (storage.Subagent, *agentruntime.Run, error) {
	record, err := server.ownedSubagent(c)
	if err != nil {
		return storage.Subagent{}, nil, err
	}
	run, err := server.agent.SubagentRun(c.Request().Context(), c.Param("parentSessionID"), record.ID, c.Param("turnID"))
	if err != nil {
		return storage.Subagent{}, nil, err
	}
	return record, run, nil
}

func newSubagentDefinitionResponse(definition SubagentDefinition) SubagentDefinitionResponse {
	return SubagentDefinitionResponse{Name: definition.Name, Description: definition.Description, Provider: definition.Provider, Model: definition.Model, Skills: append([]string{}, definition.Skills...), Tools: append([]string{}, definition.Tools...)}
}

func newSubagentResponse(record storage.Subagent) SubagentResponse {
	return SubagentResponse{ID: record.ID, DisplayName: record.DisplayName, Label: record.Label, ParentSessionID: record.ParentSessionID, ParentTurnID: record.ParentTurnID, SessionID: record.SessionID, DefinitionName: record.DefinitionName, Provider: record.Provider, Model: record.Model, Status: record.Status, CurrentTurnID: record.CurrentTurnID, LastTurnID: record.LastTurnID, LastTurnError: record.LastTurnError, Version: record.Version, QueuedMessages: len(record.Pending), CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, ClosedAt: record.ClosedAt}
}

func newSubagentResponses(records []storage.Subagent) []SubagentResponse {
	responses := make([]SubagentResponse, len(records))
	for index, record := range records {
		responses[index] = newSubagentResponse(record)
	}
	return responses
}

func newMessageResponses(messages []agentruntime.Message) []MessageResponse {
	responses := make([]MessageResponse, len(messages))
	for index, message := range messages {
		responses[index] = newMessageResponse(message)
	}
	return responses
}

func newTurnResponse(run *agentruntime.Run) TurnResponse {
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
	return response
}

func subagentPath(parentSessionID, subagentID string) string {
	return "/v1/sessions/" + url.PathEscape(parentSessionID) + "/subagents/" + url.PathEscape(subagentID)
}

func subagentTurnPath(parentSessionID, subagentID, turnID string) string {
	return subagentPath(parentSessionID, subagentID) + "/turns/" + url.PathEscape(turnID)
}
