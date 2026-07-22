package agentcli

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"
)

const callbackDeliveryRepairReminder = `This turn contains an authoritative subagent callback but no user-visible assistant response after it. Do not call any tools. Deliver the callback's final_answer or summary now, including any unresolved next_step or error. If the child was already closed, report the result without discussing cleanup. Do not repeat text that was already delivered.`

// callbackDeliveryCompletionGuard gives a root callback turn one bounded,
// tool-free provider round when it tries to end without user-visible content.
// Content emitted alongside a tool call counts as delivered, preventing a
// post-close repair from repeating an answer the user already saw.
func callbackDeliveryCompletionGuard(_ context.Context, attempt agentruntime.CompletionAttempt) (agentruntime.CompletionDecision, error) {
	callbackIndex := latestSubagentCallbackIndex(attempt.TurnID, attempt.Messages)
	if callbackIndex < 0 || callbackDelivered(attempt.TurnID, attempt.Messages[callbackIndex+1:]) || attempt.RepairCount > 0 {
		return agentruntime.CompletionDecision{Action: agentruntime.CompletionProceed}, nil
	}
	return agentruntime.CompletionDecision{
		Action:           agentruntime.CompletionRetry,
		ContextReminders: []agentruntime.ContextReminder{{Content: callbackDeliveryRepairReminder}},
		ToolAllowlist:    []string{},
	}, nil
}

func latestSubagentCallbackIndex(turnID string, messages []agentruntime.Message) int {
	latest := -1
	for index, message := range messages {
		if message.TurnID != turnID || message.Type != agentruntime.MessageTypeRuntimeEvent {
			continue
		}
		if isSubagentCallbackMessage(message.Content) {
			latest = index
		}
	}
	return latest
}

func isSubagentCallbackMessage(content string) bool {
	const opening = "<subagent_callback>"
	const closing = "</subagent_callback>"
	start := strings.Index(content, opening)
	end := strings.LastIndex(content, closing)
	if start < 0 || end <= start+len(opening) {
		return false
	}
	var envelope struct {
		ID     string                 `json:"id"`
		Status SubagentCallbackStatus `json:"status"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(content[start+len(opening):end])), &envelope) != nil {
		return false
	}
	return strings.TrimSpace(envelope.ID) != "" && (envelope.Status == SubagentCallbackCompleted || envelope.Status == SubagentCallbackIncomplete || envelope.Status == SubagentCallbackFailed)
}

func callbackDelivered(turnID string, messages []agentruntime.Message) bool {
	for _, message := range messages {
		if message.TurnID != turnID || strings.TrimSpace(message.Content) == "" {
			continue
		}
		if message.Type == agentruntime.MessageTypeAssistant || message.Type == agentruntime.MessageTypeToolCall {
			return true
		}
	}
	return false
}
