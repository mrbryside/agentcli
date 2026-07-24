package agentcli

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
)

func completionGuardWithRequiredTools(base agentruntime.CompletionGuard, required []string) agentruntime.CompletionGuard {
	required = append([]string(nil), required...)
	var mu sync.Mutex
	type repairProgress struct {
		missing    []string
		noProgress int
	}
	progress := make(map[string]repairProgress)
	return func(ctx context.Context, attempt agentruntime.CompletionAttempt) (agentruntime.CompletionDecision, error) {
		progressKey := attempt.SessionID + "\x00" + attempt.TurnID
		missing := missingRequiredTools(attempt.TurnID, attempt.Messages, required, attempt.TerminalToolBatch)
		baseDecision := agentruntime.CompletionDecision{Action: agentruntime.CompletionProceed}
		var err error
		if base != nil {
			baseDecision, err = base(ctx, attempt)
			if err != nil {
				return agentruntime.CompletionDecision{}, err
			}
		}
		if len(missing) == 0 {
			mu.Lock()
			delete(progress, progressKey)
			mu.Unlock()
			return baseDecision, nil
		}
		mu.Lock()
		state := progress[progressKey]
		if len(state.missing) > len(missing) {
			state.noProgress = 0
		}
		state.noProgress++
		state.missing = append(state.missing[:0], missing...)
		progress[progressKey] = state
		progressAttempts := state.noProgress
		exhausted := state.noProgress > defaultCompletionRepairLimit
		if exhausted {
			delete(progress, progressKey)
		}
		mu.Unlock()
		if exhausted {
			return agentruntime.CompletionDecision{}, fmt.Errorf(
				"required end-of-turn tool was not called successfully after %d repair attempts without progress: %s",
				defaultCompletionRepairLimit,
				strings.Join(missing, ", "),
			)
		}

		decision := agentruntime.CompletionDecision{
			Action: agentruntime.CompletionRetry,
			ContextReminders: []agentruntime.ContextReminder{{Content: fmt.Sprintf(
				"This turn cannot finish until every required finalizer tool has succeeded. Call all of these tools now, in the same response, using the completed work to construct their arguments: %s. Do not emit a user-facing assistant message before the finalizer tool call. Do not repeat prior work or any already-successful tool call. This is repair attempt %d of %d; keep calling the required tool on the next repair if this attempt does not produce a successful result.",
				strings.Join(missing, ", "), progressAttempts, defaultCompletionRepairLimit,
			)}},
			ToolAllowlist:    append([]string(nil), missing...),
			NormalToolChoice: &agentruntime.ToolChoice{Mode: agentruntime.ToolChoiceRequired},
		}
		if len(missing) == 1 {
			decision.ToolChoice = &agentruntime.ToolChoice{
				Mode: agentruntime.ToolChoiceSpecific,
				Name: missing[0],
			}
		} else {
			decision.ToolChoice = &agentruntime.ToolChoice{Mode: agentruntime.ToolChoiceRequired}
		}
		if baseDecision.Action == agentruntime.CompletionRetry {
			decision.ContextReminders = append(decision.ContextReminders, baseDecision.ContextReminders...)
			decision.ToolAllowlist = unionToolNames(decision.ToolAllowlist, baseDecision.ToolAllowlist)
			decision.NormalToolChoice = mergeRepairToolChoices(decision.NormalToolChoice, baseDecision.NormalToolChoice)
			decision.ToolChoice = mergeRepairToolChoices(decision.ToolChoice, baseDecision.ToolChoice)
		}
		return decision, nil
	}
}

func requiredToolChoiceProvider(required []string) agentruntime.ToolChoiceProvider {
	required = append([]string(nil), required...)
	return func(_ context.Context, attempt agentruntime.CompletionAttempt) (*agentruntime.ToolChoice, error) {
		if len(required) == 0 || len(missingRequiredTools(attempt.TurnID, attempt.Messages, required, attempt.TerminalToolBatch)) == 0 {
			return nil, nil
		}
		return &agentruntime.ToolChoice{Mode: agentruntime.ToolChoiceRequired}, nil
	}
}

func mergeRepairToolChoices(first, second *agentruntime.ToolChoice) *agentruntime.ToolChoice {
	if first == nil {
		if second == nil {
			return nil
		}
		clone := *second
		return &clone
	}
	if second == nil || (first.Mode == second.Mode && first.Name == second.Name) {
		clone := *first
		return &clone
	}
	return &agentruntime.ToolChoice{Mode: agentruntime.ToolChoiceRequired}
}

func missingRequiredTools(turnID string, messages []agentruntime.Message, required []string, terminalToolBatch bool) []string {
	// Only the terminal result batch can finalize a turn. A successful required
	// call from an earlier continuing round is deliberately ignored.
	if !terminalToolBatch {
		return append([]string(nil), required...)
	}
	start := len(messages)
	for start > 0 {
		message := messages[start-1]
		if message.TurnID != turnID || message.Type != agentruntime.MessageTypeToolResult || message.ToolResult == nil {
			break
		}
		start--
	}
	succeeded := make(map[string]struct{}, len(required))
	for _, message := range messages[start:] {
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
