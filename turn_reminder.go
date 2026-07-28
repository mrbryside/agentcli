package agentcli

import (
	"context"
	"fmt"
	"html"

	"github.com/mrbryside/agentcli/agentruntime"
)

func newTurnContextReminderProvider() agentruntime.ContextReminderProvider {
	return func(_ context.Context, request agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		if request.ProviderStep != 0 {
			return nil, nil
		}
		return []agentruntime.ContextReminder{{Content: fmt.Sprintf(`<turn_start>
state: new
turn_id: %q
instruction: This is the first model request of a new turn. Evaluate the new user message or delivered subagent result now. Later model requests without <turn_start> belong to this same turn. Tool results do not start another turn.
</turn_start>`, html.EscapeString(request.TurnID))}}, nil
	}
}
