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

// subagentRoutes are host session-management endpoints. They intentionally
// remain nested under their owning main-agent session: a subagent ID alone is
// never enough authority to access a transcript, retained run, or permission
// request. They do not expose the main model's task protocol.
func (server *Server) subagentRoutes() {
	server.echo.GET("/v1/subagent-definitions", server.listSubagentDefinitions)
	server.echo.POST("/v1/sessions/:mainAgentSessionID/subagents", server.createSubagent)
	server.echo.GET("/v1/sessions/:mainAgentSessionID/subagents", server.listSubagents)
	server.echo.GET("/v1/sessions/:mainAgentSessionID/subagents/:subagentID", server.getSubagent)
	server.echo.DELETE("/v1/sessions/:mainAgentSessionID/subagents/:subagentID", server.closeSubagent)
	server.echo.POST("/v1/sessions/:mainAgentSessionID/subagents/:subagentID/turns", server.sendSubagentTurn)
	server.echo.GET("/v1/sessions/:mainAgentSessionID/subagents/:subagentID/messages", server.listSubagentMessages)
	server.echo.GET("/v1/sessions/:mainAgentSessionID/subagents/:subagentID/turns/:turnID", server.getSubagentTurn)
	server.echo.GET("/v1/sessions/:mainAgentSessionID/subagents/:subagentID/turns/:turnID/events", server.streamSubagentTurn)
	server.echo.POST("/v1/sessions/:mainAgentSessionID/subagents/:subagentID/turns/:turnID/interrupt", server.interruptSubagentTurn)
	server.echo.POST("/v1/sessions/:mainAgentSessionID/subagents/:subagentID/permissions/:permissionID/decisions", server.resolveSubagentPermission)
	server.echo.POST("/v1/sessions/:mainAgentSessionID/subagents/:subagentID/confirmations/:confirmationID/decisions", server.resolveSubagentConfirmation)
	server.echo.GET("/v1/sessions/:mainAgentSessionID/subagent-confirmations", server.listPendingSubagentConfirmations)
	server.echo.GET("/v1/sessions/:mainAgentSessionID/subagent-permissions", server.listPendingSubagentPermissions)
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
// @Summary Create and asynchronously start a host-managed subagent session
// @ID createSubagent
// @Tags Subagents
// @Accept json
// @Produce json
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param request body CreateSubagentRequest true "Subagent definition and initial message"
// @Success 201 {object} SubagentResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 500 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents [post]
func (server *Server) createSubagent(c echo.Context) error {
	mainAgentSessionID := c.Param("mainAgentSessionID")
	if strings.TrimSpace(mainAgentSessionID) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "main agent session ID is required")
	}
	var body CreateSubagentRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Message) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "name and message are required")
	}
	mainAgentTurnID := strings.TrimSpace(body.MainAgentTurnID)
	if mainAgentTurnID == "" {
		var err error
		mainAgentTurnID, err = newSubagentID("turn_")
		if err != nil {
			return server.writeRuntimeError(c, err)
		}
	}
	record, err := server.agent.StartSubagent(c.Request().Context(), mainAgentSessionID, mainAgentTurnID, body.Name, body.Message, body.Label)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	response := newSubagentResponse(record)
	c.Response().Header().Set("Location", subagentPath(mainAgentSessionID, record.ID))
	return writeJSON(c, http.StatusCreated, response)
}

// listSubagents godoc
// @Summary List host-managed subagent sessions owned by a main agent session
// @ID listSubagents
// @Tags Subagents
// @Produce json
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param include_closed query boolean false "Include closed subagent records"
// @Success 200 {object} SubagentsResponse
// @Failure 400 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents [get]
func (server *Server) listSubagents(c echo.Context) error {
	mainAgentSessionID := c.Param("mainAgentSessionID")
	if strings.TrimSpace(mainAgentSessionID) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "main agent session ID is required")
	}
	includeClosed := c.QueryParam("include_closed") == "true"
	records, err := server.agent.ListSubagents(c.Request().Context(), mainAgentSessionID, includeClosed)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, SubagentsResponse{Subagents: newSubagentResponses(records)})
}

// getSubagent godoc
// @Summary Read one host-managed subagent session
// @ID getSubagent
// @Tags Subagents
// @Produce json
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param subagentID path string true "Subagent ID"
// @Success 200 {object} SubagentResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents/{subagentID} [get]
func (server *Server) getSubagent(c echo.Context) error {
	record, err := server.ownedSubagent(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, newSubagentResponse(record))
}

// closeSubagent godoc
// @Summary Close one owned subagent
// @Description Destructively closes one subagent, interrupting active work, dropping queued input, and cancelling outstanding response-scope result obligations when necessary, while retaining transcript and completed event history. Bind this endpoint to an explicit user action. A successful close also emits subagent_closed on the main agent session event stream.
// @ID closeSubagent
// @Tags Subagents
// @Produce json
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param subagentID path string true "Subagent ID"
// @Success 200 {object} SubagentResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents/{subagentID} [delete]
func (server *Server) closeSubagent(c echo.Context) error {
	mainAgentSessionID, subagentID := c.Param("mainAgentSessionID"), c.Param("subagentID")
	if strings.TrimSpace(mainAgentSessionID) == "" || strings.TrimSpace(subagentID) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "main agent session and subagent IDs are required")
	}
	record, err := server.agent.CloseSubagent(c.Request().Context(), mainAgentSessionID, subagentID)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, newSubagentResponse(record))
}

// sendSubagentTurn godoc
// @Summary Continue a host-managed subagent conversation
// @ID startSubagentTurn
// @Description Queues the message while the subagent is running, or starts an idle incomplete/completed/failed subagent. Closed subagents return conflict. An immediately started turn can be streamed with Accept: text/event-stream.
// @Tags Subagents
// @Accept json
// @Produce json
// @Produce text/event-stream
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param request body SendSubagentMessageRequest true "Next user message"
// @Success 202 {object} SubagentResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns [post]
func (server *Server) sendSubagentTurn(c echo.Context) error {
	mainAgentSessionID, subagentID := c.Param("mainAgentSessionID"), c.Param("subagentID")
	if strings.TrimSpace(mainAgentSessionID) == "" || strings.TrimSpace(subagentID) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "main agent session and subagent IDs are required")
	}
	var body SendSubagentMessageRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	if strings.TrimSpace(body.Message) == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "message is required")
	}
	record, err := server.agent.SendSubagentMessage(c.Request().Context(), mainAgentSessionID, subagentID, body.Message)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	response := newSubagentResponse(record)
	status := http.StatusAccepted
	if record.CurrentSubagentTurnID != "" && record.Status == storage.SubagentStatusRunning {
		c.Response().Header().Set("Location", subagentTurnPath(mainAgentSessionID, subagentID, record.CurrentSubagentTurnID))
		// A new idle subagent turn can be streamed from the same POST ergonomics as
		// a main-agent turn. Queued mailbox work has no run of its own yet, so it is
		// represented by the normal accepted summary instead.
		if acceptsEventStream(c.Request()) && len(record.Pending) == 0 {
			run, runErr := server.agent.SubagentRun(c.Request().Context(), mainAgentSessionID, subagentID, record.CurrentSubagentTurnID)
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
// @Description Reading for UI rendering does not mark subagent activity as observed by the main agent model.
// @Tags Subagents
// @Produce json
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param subagentID path string true "Subagent ID"
// @Success 200 {object} SubagentMessagesResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/messages [get]
func (server *Server) listSubagentMessages(c echo.Context) error {
	record, err := server.ownedSubagent(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	// UI reads deliberately bypass ReadSubagent: rendering a nested chat must
	// not mark activity as observed by the main agent model.
	messages, err := server.agent.ListMessages(c.Request().Context(), record.SubagentSessionID)
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
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param turnID path string true "Subagent turn ID"
// @Success 200 {object} SubagentTurnResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns/{turnID} [get]
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
// @Description Replays retained subagent-turn events after the cursor, then continues with live EventResponse envelopes.
// @Tags Event streams
// @Produce json
// @Produce text/event-stream
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param turnID path string true "Subagent turn ID"
// @Param after query integer false "Resume after this event sequence"
// @Param Last-Event-ID header integer false "Resume after this event sequence when after is omitted"
// @Success 200 {object} EventResponse "One SSE data payload; the HTTP response remains open for more events"
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns/{turnID}/events [get]
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
// @Description Only the subagent turn currently active may be interrupted. The body is optional.
// @Tags Subagents
// @Accept json
// @Produce json
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param turnID path string true "Subagent turn ID"
// @Param request body InterruptRequest false "Optional interruption reason"
// @Success 202 {object} SubagentTurnResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/turns/{turnID}/interrupt [post]
func (server *Server) interruptSubagentTurn(c echo.Context) error {
	mainAgentSessionID, subagentID := c.Param("mainAgentSessionID"), c.Param("subagentID")
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
	// The manager interrupt operation is intentionally subagent-scoped because it
	// owns the active run gate. Do not let a historical turn URL interrupt a
	// newer active turn for the same subagent.
	if record.CurrentSubagentTurnID == run.TurnID() {
		if err := server.agent.InterruptSubagent(c.Request().Context(), mainAgentSessionID, subagentID, body.Reason); err != nil {
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
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param permissionID path string true "Permission request ID"
// @Param request body PermissionDecisionRequest true "Permission decision"
// @Success 200 {object} PermissionDecisionResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/permissions/{permissionID}/decisions [post]
func (server *Server) resolveSubagentPermission(c echo.Context) error {
	mainAgentSessionID, subagentID := c.Param("mainAgentSessionID"), c.Param("subagentID")
	permissionID := permission.ID(c.Param("permissionID"))
	if strings.TrimSpace(mainAgentSessionID) == "" || strings.TrimSpace(subagentID) == "" || permissionID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "main agent session, subagent, and permission IDs are required")
	}
	// Check ownership before accepting the decision; this must never route a
	// main-agent-session decision into a subagent or across another main agent's subagent.
	record, err := server.ownedSubagent(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	var body PermissionDecisionRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	decision := permission.Decision{PermissionID: permissionID, SessionID: record.SubagentSessionID, TurnID: body.TurnID, CallID: body.CallID, Type: body.Decision}
	if body.SessionID != "" && body.SessionID != record.SubagentSessionID {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "permission session does not match subagent")
	}
	if err := server.agent.ResolveSubagentPermission(c.Request().Context(), mainAgentSessionID, subagentID, decision); err != nil {
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
// @Param mainAgentSessionID path string true "Owning main agent session ID"
// @Param subagentID path string true "Subagent ID"
// @Param confirmationID path string true "Confirmation request ID"
// @Param request body ConfirmationDecisionRequest true "Confirmation answer"
// @Success 200 {object} ConfirmationDecisionResponse
// @Failure 400 {object} APIErrorResponse
// @Failure 404 {object} APIErrorResponse
// @Failure 409 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagents/{subagentID}/confirmations/{confirmationID}/decisions [post]
func (server *Server) resolveSubagentConfirmation(c echo.Context) error {
	mainAgentSessionID, subagentID := c.Param("mainAgentSessionID"), c.Param("subagentID")
	confirmationID := confirmation.ID(c.Param("confirmationID"))
	if strings.TrimSpace(mainAgentSessionID) == "" || strings.TrimSpace(subagentID) == "" || confirmationID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "main agent session, subagent, and confirmation IDs are required")
	}
	record, err := server.ownedSubagent(c)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	var body ConfirmationDecisionRequest
	if err := server.decodeJSON(c, &body); err != nil {
		return server.writeDecodeError(c, err)
	}
	if body.SessionID != "" && body.SessionID != record.SubagentSessionID {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "confirmation session does not match subagent")
	}
	decision := confirmation.Decision{ConfirmationID: confirmationID, SessionID: record.SubagentSessionID, TurnID: body.TurnID, CallID: body.CallID, Answer: body.Answer}
	if err := server.agent.ResolveSubagentConfirmation(c.Request().Context(), mainAgentSessionID, subagentID, decision); err != nil {
		return server.writeRuntimeError(c, err)
	}
	return writeJSON(c, http.StatusOK, ConfirmationDecisionResponse{Decision: newConfirmationDecisionResponse(decision)})
}

func (server *Server) ownedSubagent(c echo.Context) (storage.Subagent, error) {
	manager, err := server.agent.subagentManager()
	if err != nil {
		return storage.Subagent{}, err
	}
	return manager.getOwned(c.Request().Context(), c.Param("mainAgentSessionID"), c.Param("subagentID"))
}

func (server *Server) ownedSubagentRun(c echo.Context) (storage.Subagent, *agentruntime.Run, error) {
	record, err := server.ownedSubagent(c)
	if err != nil {
		return storage.Subagent{}, nil, err
	}
	run, err := server.agent.SubagentRun(c.Request().Context(), c.Param("mainAgentSessionID"), record.ID, c.Param("turnID"))
	if err != nil {
		return storage.Subagent{}, nil, err
	}
	return record, run, nil
}

func newSubagentDefinitionResponse(definition SubagentDefinition) SubagentDefinitionResponse {
	return SubagentDefinitionResponse{Name: definition.Name, Description: definition.Description, Provider: definition.Provider, Model: definition.Model, Skills: append([]string{}, definition.Skills...), Tools: append([]string{}, definition.Tools...)}
}

func newSubagentResponse(record storage.Subagent) SubagentResponse {
	return SubagentResponse{ID: record.ID, DisplayName: record.DisplayName, Label: record.Label, MainAgentSessionID: record.MainAgentSessionID, MainAgentTurnID: record.MainAgentTurnID, SubagentSessionID: record.SubagentSessionID, DefinitionName: record.DefinitionName, Provider: record.Provider, Model: record.Model, Status: record.Status, CurrentSubagentTurnID: record.CurrentSubagentTurnID, LastSubagentTurnID: record.LastSubagentTurnID, LastResultError: record.LastResultError, LastResultStatus: record.LastResultStatus, LastResultSummary: record.LastResultSummary, LastResultNextStep: record.LastResultNextStep, Version: record.Version, QueuedMessages: len(record.Pending), CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, ClosedAt: record.ClosedAt}
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

func subagentPath(mainAgentSessionID, subagentID string) string {
	return "/v1/sessions/" + url.PathEscape(mainAgentSessionID) + "/subagents/" + url.PathEscape(subagentID)
}

func subagentTurnPath(mainAgentSessionID, subagentID, turnID string) string {
	return subagentPath(mainAgentSessionID, subagentID) + "/turns/" + url.PathEscape(turnID)
}
