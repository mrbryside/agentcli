package agentcli

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func (server *Server) forwardSubagentConfirmations(events <-chan SubagentConfirmationEvent) {
	for {
		select {
		case <-server.context.Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			server.sessionEvents.publish(SessionEventResponse{
				Type:                 SessionActivitySubagentConfirmation,
				Source:               ServerTurnSourceSubagentConfirmation,
				SessionID:            event.MainAgentSessionID,
				TurnID:               event.MainAgentTurnID,
				SubagentConfirmation: newSubagentConfirmationReference(event),
			})
		}
	}
}

func newSubagentConfirmationReference(event SubagentConfirmationEvent) *SubagentConfirmationReference {
	reference := &SubagentConfirmationReference{
		Type: event.Type, SubagentID: event.SubagentID, DisplayName: event.DisplayName,
		DefinitionName: event.DefinitionName, SubagentSessionID: event.SubagentSessionID, SubagentTurnID: event.SubagentTurnID,
	}
	if event.Request != nil {
		request := newConfirmationRequestResponse(*event.Request)
		reference.Confirmation = &request
	}
	if event.Decision != nil {
		decision := newConfirmationDecisionResponse(*event.Decision)
		reference.Decision = &decision
	}
	return reference
}

// listPendingSubagentConfirmations godoc
// @Summary List pending confirmations for a main agent session
// @ID listPendingSubagentConfirmations
// @Description Returns durable pending subagent confirmation requests so clients can recover after attaching late or reconnecting.
// @Tags Confirmations
// @Produce json
// @Param mainAgentSessionID path string true "Main agent session ID"
// @Success 200 {object} PendingSubagentConfirmationsResponse
// @Failure 400 {object} APIErrorResponse
// @Router /v1/sessions/{mainAgentSessionID}/subagent-confirmations [get]
func (server *Server) listPendingSubagentConfirmations(c echo.Context) error {
	mainAgentSessionID := c.Param("mainAgentSessionID")
	if mainAgentSessionID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "main agent session ID is required")
	}
	events, err := server.agent.PendingSubagentConfirmations(c.Request().Context(), mainAgentSessionID)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	response := PendingSubagentConfirmationsResponse{Confirmations: make([]SubagentConfirmationReference, 0, len(events))}
	for _, event := range events {
		response.Confirmations = append(response.Confirmations, *newSubagentConfirmationReference(event))
	}
	return writeJSON(c, http.StatusOK, response)
}
