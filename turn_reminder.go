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
		return []agentruntime.ContextReminder{{Content: fmt.Sprintf(`<runtime_turn_boundary>
state: new_turn
turn_id: %q
provider_step: 1
instruction: This reminder appears only on the first provider request of a new runtime turn. Evaluate turn-scoped triggers for the newly delivered user message or runtime event now. Later provider requests without this marker continue this same runtime turn; a tool result or later provider step does not create another load trigger by itself.
</runtime_turn_boundary>`, html.EscapeString(request.TurnID))}}, nil
	}
}
