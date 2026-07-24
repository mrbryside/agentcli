package agentcli

import (
	"context"

	"github.com/mrbryside/agentcli/agentruntime"
)

const subagentOutcomeRepairReminder = `This child turn attempted to finish without a successful report_subagent_outcome result. Do not repeat the delegated work and do not repeat any domain action or tool call. Review the existing messages and tool results, then call report_subagent_outcome exactly once. Use completed only when every required action is resolved and no required work remains. Otherwise use incomplete with a concrete next_step. This is a bounded repair loop; call the outcome tool again on the next repair if this attempt does not produce a successful result.`

// subagentOutcomeCompletionGuard gives a child a few bounded opportunities to
// repair a missing semantic outcome before its callback becomes visible. The
// retry keeps the normal child tool catalog available and reminds the child
// not to repeat an already-completed domain action during repair.
func subagentOutcomeCompletionGuard(_ context.Context, attempt agentruntime.CompletionAttempt) (agentruntime.CompletionDecision, error) {
	if _, found := reportedSubagentOutcome(attempt.TurnID, attempt.Messages); found || attempt.RepairCount >= defaultCompletionRepairLimit {
		return agentruntime.CompletionDecision{Action: agentruntime.CompletionProceed}, nil
	}
	return agentruntime.CompletionDecision{
		Action:           agentruntime.CompletionRetry,
		ContextReminders: []agentruntime.ContextReminder{{Content: subagentOutcomeRepairReminder}},
	}, nil
}
