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
// an empty non-nil allowlist exposes no tools. ToolChoice can force the next
// repair request, while NormalToolChoice applies to its later provider rounds.
type CompletionDecision struct {
	Action           CompletionAction
	ContextReminders []ContextReminder
	ToolAllowlist    []string
	// NormalToolChoice applies after the first repair request.
	NormalToolChoice *ToolChoice
	// ToolChoice applies to the next repair request only.
	ToolChoice *ToolChoice
}

// CompletionGuard can defer terminal completion after persisted output has
// become available for inspection. It is called serially by the run owner.
type CompletionGuard func(context.Context, CompletionAttempt) (CompletionDecision, error)

func validateCompletionDecision(decision CompletionDecision, available []ToolDefinition) error {
	switch decision.Action {
	case CompletionProceed:
		if len(decision.ContextReminders) != 0 || decision.ToolAllowlist != nil || decision.NormalToolChoice != nil || decision.ToolChoice != nil {
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
	if decision.ToolChoice != nil {
		if err := decision.ToolChoice.Validate(); err != nil {
			return fmt.Errorf("retry tool choice: %w", err)
		}
		if decision.ToolChoice.Mode == ToolChoiceRequired && decision.ToolAllowlist != nil && len(decision.ToolAllowlist) == 0 {
			return fmt.Errorf("required tool choice cannot use an empty tool allowlist")
		}
		if decision.ToolChoice.Mode == ToolChoiceSpecific {
			found := false
			for _, tool := range available {
				if tool.Name == decision.ToolChoice.Name {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("retry tool choice references unknown tool %q", decision.ToolChoice.Name)
			}
			if decision.ToolAllowlist != nil {
				found = false
				for _, name := range decision.ToolAllowlist {
					if name == decision.ToolChoice.Name {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("retry tool choice %q is not in the tool allowlist", decision.ToolChoice.Name)
				}
			}
		}
	}
	if decision.NormalToolChoice != nil {
		if err := decision.NormalToolChoice.Validate(); err != nil {
			return fmt.Errorf("normal retry tool choice: %w", err)
		}
		if decision.NormalToolChoice.Mode == ToolChoiceRequired && decision.ToolAllowlist != nil && len(decision.ToolAllowlist) == 0 {
			return fmt.Errorf("normal required tool choice cannot use an empty tool allowlist")
		}
		if decision.NormalToolChoice.Mode == ToolChoiceSpecific {
			return fmt.Errorf("normal retry tool choice cannot be specific")
		}
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
	if decision.ToolChoice != nil {
		choice := *decision.ToolChoice
		decision.ToolChoice = &choice
	}
	if decision.NormalToolChoice != nil {
		choice := *decision.NormalToolChoice
		decision.NormalToolChoice = &choice
	}
	return decision
}
