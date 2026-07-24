package agentruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/mrbryside/agentcli/storage"
)

// CompletionAction tells Runtime whether a provider-complete turn may become
// terminal or needs one more provider round first.
type CompletionAction string

const (
	CompletionProceed CompletionAction = "proceed"
	CompletionRetry   CompletionAction = "retry"
)

// CompletionAttempt is a defensive snapshot taken after the provider's
// latest assistant message or terminal tool batch has been persisted.
type CompletionAttempt struct {
	SessionID string
	TurnID    string
	Messages  []Message
	// TerminalToolBatch is true only when Messages ends in the current
	// successful all-EndTurn tool batch. It distinguishes that batch from a
	// prior or mixed continuing round.
	TerminalToolBatch bool
	ProviderSteps     int
	RepairCount       int
}

// CompletionDecision is the pure result of a CompletionGuard. Retry reminders
// are injected only into the next provider request and are never persisted.
// A non-nil ToolAllowlist restricts every provider round after retry begins;
// an empty non-nil allowlist exposes no tools.
type CompletionDecision struct {
	Action           CompletionAction
	ContextReminders []ContextReminder
	ToolAllowlist    []string
}

// CompletionGuard can defer terminal completion after persisted output has
// become available for inspection. It is called serially by the run owner.
type CompletionGuard func(context.Context, CompletionAttempt) (CompletionDecision, error)

func validateCompletionDecision(decision CompletionDecision, available []ToolDefinition) error {
	switch decision.Action {
	case CompletionProceed:
		if len(decision.ContextReminders) != 0 || decision.ToolAllowlist != nil {
			return fmt.Errorf("proceed completion decision cannot include retry configuration")
		}
		return nil
	case CompletionRetry:
	default:
		return fmt.Errorf("unknown completion action %q", decision.Action)
	}

	if len(decision.ContextReminders) == 0 {
		return fmt.Errorf("retry completion decision requires a context reminder")
	}
	for index, reminder := range decision.ContextReminders {
		if strings.TrimSpace(reminder.Content) == "" {
			return fmt.Errorf("retry completion reminder %d is empty", index)
		}
	}
	if decision.ToolAllowlist == nil {
		return nil
	}
	known := make(map[string]struct{}, len(available))
	for _, tool := range available {
		known[tool.Name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(decision.ToolAllowlist))
	for index, name := range decision.ToolAllowlist {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("completion retry tool allowlist entry %d is empty", index)
		}
		if _, found := known[name]; !found {
			return fmt.Errorf("completion retry references unknown tool %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("completion retry repeats tool %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func cloneCompletionAttempt(attempt CompletionAttempt) CompletionAttempt {
	attempt.Messages = storage.CloneMessages(attempt.Messages)
	return attempt
}

func cloneCompletionDecision(decision CompletionDecision) CompletionDecision {
	decision.ContextReminders = cloneContextReminders(decision.ContextReminders)
	if decision.ToolAllowlist != nil {
		allowlist := make([]string, len(decision.ToolAllowlist))
		copy(allowlist, decision.ToolAllowlist)
		decision.ToolAllowlist = allowlist
	}
	return decision
}
