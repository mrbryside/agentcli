package agentcli

import (
	"context"
	"fmt"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"
)

func completionGuardWithRequiredTools(base agentruntime.CompletionGuard, required []string) agentruntime.CompletionGuard {
	required = append([]string(nil), required...)
	return func(ctx context.Context, attempt agentruntime.CompletionAttempt) (agentruntime.CompletionDecision, error) {
		missing := missingRequiredTools(attempt.TurnID, attempt.Messages, required)
		baseDecision := agentruntime.CompletionDecision{Action: agentruntime.CompletionProceed}
		var err error
		if base != nil {
			baseDecision, err = base(ctx, attempt)
			if err != nil {
				return agentruntime.CompletionDecision{}, err
			}
		}
		if len(missing) == 0 {
			return baseDecision, nil
		}
		if attempt.RepairCount > 0 {
			return agentruntime.CompletionDecision{}, fmt.Errorf(
				"required end-of-turn tool was not called successfully after repair: %s",
				strings.Join(missing, ", "),
			)
		}

		decision := agentruntime.CompletionDecision{
			Action: agentruntime.CompletionRetry,
			ContextReminders: []agentruntime.ContextReminder{{Content: fmt.Sprintf(
				"This turn cannot finish until every required finalizer tool has succeeded. Call all of these tools now, in the same response, using the completed work to construct their arguments: %s. Do not repeat prior work or any already-successful tool call. This is the only repair opportunity.",
				strings.Join(missing, ", "),
			)}},
			ToolAllowlist: append([]string(nil), missing...),
		}
		if baseDecision.Action == agentruntime.CompletionRetry {
			decision.ContextReminders = append(decision.ContextReminders, baseDecision.ContextReminders...)
			decision.ToolAllowlist = unionToolNames(decision.ToolAllowlist, baseDecision.ToolAllowlist)
		}
		return decision, nil
	}
}

func missingRequiredTools(turnID string, messages []agentruntime.Message, required []string) []string {
	succeeded := make(map[string]struct{}, len(required))
	for _, message := range messages {
		if message.TurnID != turnID || message.Type != agentruntime.MessageTypeToolResult || message.ToolResult == nil {
			continue
		}
		if message.ToolResult.Status == agentruntime.ToolResultSucceeded {
			succeeded[message.ToolResult.Name] = struct{}{}
		}
	}
	missing := make([]string, 0, len(required))
	for _, name := range required {
		if _, found := succeeded[name]; !found {
			missing = append(missing, name)
		}
	}
	return missing
}

func unionToolNames(first, second []string) []string {
	names := make([]string, 0, len(first)+len(second))
	seen := make(map[string]struct{}, len(first)+len(second))
	for _, list := range [][]string{first, second} {
		for _, name := range list {
			if _, found := seen[name]; found {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	return names
}
