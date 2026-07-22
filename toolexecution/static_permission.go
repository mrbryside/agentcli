package toolexecution

import (
	"encoding/json"
	"time"

	"github.com/mrbryside/agentcli/permission"
)

// PermissionConfig describes a fixed permission requirement for a custom
// tool. StaticPermission copies Actions so callers can safely reuse or mutate
// their configuration after registration.
type PermissionConfig struct {
	Actions []permission.Action
	Risk    permission.Risk
	Reason  string
}

// StaticPermission returns a descriptor for custom tools with an unchanging
// capability requirement. The raw tool arguments are retained as Details so
// permission UIs can display exactly what the tool was asked to do.
func StaticPermission(config PermissionConfig) PermissionDescriptor {
	actions := append([]permission.Action(nil), config.Actions...)
	risk := config.Risk
	if risk == "" {
		risk = permission.RiskMedium
	}
	return func(arguments json.RawMessage) (permission.Description, error) {
		if err := permission.ValidateRequest(permission.Request{
			ID:        "static",
			SessionID: "static",
			TurnID:    "static",
			CallID:    "static",
			ToolName:  "static",
			Actions:   actions,
			Risk:      risk,
			CreatedAt: time.Unix(1, 0),
		}); err != nil {
			return permission.Description{}, err
		}
		return permission.Description{
			Actions: append([]permission.Action(nil), actions...),
			Risk:    risk,
			Details: string(arguments),
			Reason:  config.Reason,
		}, nil
	}
}
