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
				SessionID:            event.ParentSessionID,
				TurnID:               event.ParentTurnID,
				SubagentConfirmation: newSubagentConfirmationReference(event),
			})
		}
	}
}

func newSubagentConfirmationReference(event SubagentConfirmationEvent) *SubagentConfirmationReference {
	reference := &SubagentConfirmationReference{
		Type: event.Type, SubagentID: event.SubagentID, DisplayName: event.DisplayName,
		DefinitionName: event.SubagentName, ChildSessionID: event.SessionID, ChildTurnID: event.TurnID,
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
// @Summary List pending confirmations for a parent session
// @ID listPendingSubagentConfirmations
// @Description Returns durable pending child confirmation requests so clients can recover after attaching late or reconnecting.
// @Tags Confirmations
// @Produce json
// @Param parentSessionID path string true "Parent session ID"
// @Success 200 {object} PendingSubagentConfirmationsResponse
// @Failure 400 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagent-confirmations [get]
func (server *Server) listPendingSubagentConfirmations(c echo.Context) error {
	parentSessionID := c.Param("parentSessionID")
	if parentSessionID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "parent session ID is required")
	}
	events, err := server.agent.PendingSubagentConfirmations(c.Request().Context(), parentSessionID)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	response := PendingSubagentConfirmationsResponse{Confirmations: make([]SubagentConfirmationReference, 0, len(events))}
	for _, event := range events {
		response.Confirmations = append(response.Confirmations, *newSubagentConfirmationReference(event))
	}
	return writeJSON(c, http.StatusOK, response)
}
