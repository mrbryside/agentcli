package agentcli

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func (server *Server) forwardSubagentPermissions(events <-chan SubagentPermissionEvent) {
	for {
		select {
		case <-server.context.Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			server.sessionEvents.publish(SessionEventResponse{
				Type:               SessionActivitySubagentPermission,
				Source:             ServerTurnSourceSubagentPermission,
				SessionID:          event.ParentSessionID,
				TurnID:             event.ParentTurnID,
				SubagentPermission: newSubagentPermissionReference(event),
			})
		}
	}
}

func newSubagentPermissionReference(event SubagentPermissionEvent) *SubagentPermissionReference {
	reference := &SubagentPermissionReference{
		Type: event.Type, SubagentID: event.SubagentID, DisplayName: event.DisplayName,
		DefinitionName: event.SubagentName, ChildSessionID: event.SessionID, ChildTurnID: event.TurnID,
	}
	if event.Request != nil {
		request := newPermissionRequestResponse(*event.Request)
		reference.Permission = &request
	}
	if event.Decision != nil {
		decision := newDecisionResponse(*event.Decision)
		reference.Decision = &decision
	}
	return reference
}

// listPendingSubagentPermissions godoc
// @Summary List pending permissions for a parent session
// @ID listPendingSubagentPermissions
// @Description Returns durable pending child permission requests so clients can recover after attaching late or reconnecting.
// @Tags Permissions
// @Produce json
// @Param parentSessionID path string true "Parent session ID"
// @Success 200 {object} PendingSubagentPermissionsResponse
// @Failure 400 {object} APIErrorResponse
// @Router /v1/sessions/{parentSessionID}/subagent-permissions [get]
func (server *Server) listPendingSubagentPermissions(c echo.Context) error {
	parentSessionID := c.Param("parentSessionID")
	if parentSessionID == "" {
		return writeAPIError(c, http.StatusBadRequest, "invalid_request", "parent session ID is required")
	}
	events, err := server.agent.PendingSubagentPermissions(c.Request().Context(), parentSessionID)
	if err != nil {
		return server.writeRuntimeError(c, err)
	}
	response := PendingSubagentPermissionsResponse{Permissions: make([]SubagentPermissionReference, 0, len(events))}
	for _, event := range events {
		response.Permissions = append(response.Permissions, *newSubagentPermissionReference(event))
	}
	return writeJSON(c, http.StatusOK, response)
}
