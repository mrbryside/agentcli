package agentcli

import (
	"context"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/toolexecution"
)

const subagentReportRepairReminder = `The subagent tried to finish without a successful report_subagent_result call. Do not repeat the assigned work or any domain tool call. Review the existing messages and results, then call report_subagent_result exactly once. Use completed only when every required part is resolved; omit next_step and error. Use incomplete when work or information remains; include one concrete next_step and omit error. Use failed only when a terminal error prevents continuation; include the actual error and omit next_step. If this report is invalid, use the next repair attempt only to correct the report.`

// subagentReportCompletionGuard gives a subagent a few bounded opportunities to
// repair a missing semantic report before its result becomes visible. The
// required-trigger-tool wrapper restricts repair rounds to
// report_subagent_result and reminds the subagent not to repeat an
// already-completed domain action during repair.
func subagentReportCompletionGuard(_ context.Context, attempt agentruntime.CompletionAttempt) (agentruntime.CompletionDecision, error) {
	if _, found := reportedSubagentReport(attempt.TurnID, attempt.Messages); found || attempt.RepairCount >= defaultCompletionRepairLimit {
		return agentruntime.CompletionDecision{Action: agentruntime.CompletionProceed}, nil
	}
	return agentruntime.CompletionDecision{
		Action:           agentruntime.CompletionRetry,
		ContextReminders: []agentruntime.ContextReminder{{Content: subagentReportRepairReminder}},
		ToolAllowlist:    []string{toolexecution.SubagentResultToolName},
	}, nil
}
