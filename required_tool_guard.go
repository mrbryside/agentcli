package agentcli

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
)

func completionGuardWithRequiredTools(
	base agentruntime.CompletionGuard,
	requiredAtTurnEnd []string,
	requiredAtResponseScopeEnd []string,
	canonicalAtResponseScopeEnd []string,
	responseScopeReady func(string, string) bool,
) agentruntime.CompletionGuard {
	requiredAtTurnEnd = append([]string(nil), requiredAtTurnEnd...)
	requiredAtResponseScopeEnd = append([]string(nil), requiredAtResponseScopeEnd...)
	canonicalAtResponseScopeEnd = append([]string(nil), canonicalAtResponseScopeEnd...)
	var mu sync.Mutex
	type repairProgress struct {
		missing    []string
		noProgress int
	}
	progress := make(map[string]repairProgress)
	return func(ctx context.Context, attempt agentruntime.CompletionAttempt) (agentruntime.CompletionDecision, error) {
		progressKey := attempt.SessionID + "\x00" + attempt.TurnID
		required := append([]string(nil), requiredAtTurnEnd...)
		scopeReady := responseScopeReady != nil &&
			responseScopeReady(attempt.SessionID, attempt.TurnID)
		if scopeReady {
			required = append(required, requiredAtResponseScopeEnd...)
		}
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
			mu.Lock()
			delete(progress, progressKey)
			mu.Unlock()
			if !scopeReady &&
				len(requiredAtResponseScopeEnd) != 0 &&
				baseDecision.Action == agentruntime.CompletionProceed {
				baseDecision.DiscardAssistant = true
			}
			if scopeReady &&
				len(canonicalAtResponseScopeEnd) != 0 &&
				len(missingRequiredTools(attempt.TurnID, attempt.Messages, canonicalAtResponseScopeEnd)) == 0 &&
				baseDecision.Action == agentruntime.CompletionProceed {
				baseDecision.DiscardAssistant = true
			}
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

		instruction := fmt.Sprintf(
			"This turn cannot finish until every required trigger tool has succeeded. "+
				"Call all of these tools now, in the same response, using the completed work to construct their arguments: %s. "+
				"Do not emit a user-facing assistant message before the trigger tool call. "+
				"Do not repeat prior work or any already-successful tool call. "+
				"This is repair attempt %d of %d; keep calling the required tool on the next repair if this attempt does not produce a successful result.",
			strings.Join(missing, ", "), progressAttempts, defaultCompletionRepairLimit,
		)
		if scopeReady && containsAnyString(missing, requiredAtResponseScopeEnd) {
			instruction = fmt.Sprintf(
				"The response scope is ready to end. Call these required end-response-scope tools now with the final completed response: %s. "+
					"The runtime skipped any earlier calls and they did not satisfy this final trigger. "+
					"Do not repeat prior work or emit a user-facing assistant message before the tool call. "+
					"This is repair attempt %d of %d.",
				strings.Join(missing, ", "), progressAttempts, defaultCompletionRepairLimit,
			)
		}
		decision := agentruntime.CompletionDecision{
			Action:           agentruntime.CompletionRetry,
			ToolAllowlist:    append([]string(nil), missing...),
			ContextReminders: []agentruntime.ContextReminder{{Content: instruction}},
		}
		if baseDecision.Action == agentruntime.CompletionRetry {
			decision.ContextReminders = append(decision.ContextReminders, baseDecision.ContextReminders...)
			for _, toolName := range baseDecision.ToolAllowlist {
				if !containsString(decision.ToolAllowlist, toolName) {
					decision.ToolAllowlist = append(decision.ToolAllowlist, toolName)
				}
			}
		}
		return decision, nil
	}
}

func containsAnyString(values, candidates []string) bool {
	for _, candidate := range candidates {
		if containsString(values, candidate) {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func missingRequiredTools(turnID string, messages []agentruntime.Message, required []string) []string {
	requiredSet := make(map[string]struct{}, len(required))
	for _, name := range required {
		requiredSet[name] = struct{}{}
	}
	succeeded := make(map[string]bool, len(required))
	for _, message := range messages {
		if message.TurnID != turnID || message.Type != agentruntime.MessageTypeToolResult || message.ToolResult == nil {
			continue
		}
		name := message.ToolResult.Name
		if _, isRequired := requiredSet[name]; !isRequired {
			continue
		}
		// The latest attempt wins. A failed correction after an earlier success
		// must be repaired instead of silently accepting the stale invocation.
		satisfied := message.ToolResult.Status == agentruntime.ToolResultSucceeded
		if message.ToolResult.TriggerSatisfied != nil {
			satisfied = *message.ToolResult.TriggerSatisfied
		}
		succeeded[name] = satisfied
	}
	missing := make([]string, 0, len(required))
	for _, name := range required {
		if !succeeded[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
