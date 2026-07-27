package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"
)

// SubagentOutcomeToolName is a child-only framework tool. It is deliberately
// separate from the parent management-tool catalog.
const SubagentOutcomeToolName = "report_subagent_outcome"

// SubagentOutcomeStatus is the child's explicit semantic assessment of its
// delegated task, independent of the running/idle/closed lifecycle.
type SubagentOutcomeStatus string

const (
	SubagentOutcomeCompleted  SubagentOutcomeStatus = "completed"
	SubagentOutcomeIncomplete SubagentOutcomeStatus = "incomplete"
)

// SubagentOutcome is the validated report echoed into the generic transcript.
type SubagentOutcome struct {
	Status   SubagentOutcomeStatus `json:"status"`
	Summary  string                `json:"summary"`
	NextStep string                `json:"next_step,omitempty"`
}

// NewSubagentOutcomeTool returns the child-only structured completion report.
func NewSubagentOutcomeTool() Tool {
	return Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        SubagentOutcomeToolName,
			Description: "Report the child turn's semantic outcome exactly once after domain work and before the final assistant answer. This report creates the authoritative parent callback; it does not replace the child's concise final answer. Use completed only when every required part of the delegated task is resolved, with a concise evidence-grounded summary and no next_step. Use incomplete when blocked, waiting for information, partially done, or when any required work, decision, confirmation, or follow-up remains; include one concrete required next_step. Runtime/provider failure is handled separately and is not a status value. If unsure, report incomplete. After a successful report, do not call this tool or repeat domain work again; write the final answer for the parent.",
			InputSchema: mustRawToolSchema(`{"type":"object","properties":{"status":{"type":"string","enum":["completed","incomplete"],"description":"Semantic outcome: completed only when all required delegated work is resolved; otherwise incomplete."},"summary":{"type":"string","minLength":1,"description":"Concise evidence-grounded outcome for the parent callback."},"next_step":{"type":"string","minLength":1,"description":"Exactly one concrete required next step; required only when status is incomplete and forbidden when completed."}},"required":["status","summary"],"additionalProperties":false}`),
		},
		Handler: func(_ context.Context, arguments json.RawMessage) (json.RawMessage, error) {
			outcome, err := ParseSubagentOutcome(arguments)
			if err != nil {
				return nil, err
			}
			return json.Marshal(outcome)
		},
	}
}

// ParseSubagentOutcome validates the child report from tool arguments or a
// successful tool result. Unknown or ambiguous values are never completed.
func ParseSubagentOutcome(value json.RawMessage) (SubagentOutcome, error) {
	var outcome SubagentOutcome
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&outcome); err != nil {
		return SubagentOutcome{}, fmt.Errorf("decode subagent outcome: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return SubagentOutcome{}, errors.New("decode subagent outcome: multiple JSON values")
		}
		return SubagentOutcome{}, fmt.Errorf("decode subagent outcome: %w", err)
	}
	outcome.Summary = strings.TrimSpace(outcome.Summary)
	outcome.NextStep = strings.TrimSpace(outcome.NextStep)
	if outcome.Summary == "" {
		return SubagentOutcome{}, errors.New("subagent outcome summary is required")
	}
	switch outcome.Status {
	case SubagentOutcomeCompleted:
		if outcome.NextStep != "" {
			return SubagentOutcome{}, errors.New("completed subagent outcome cannot require a next step")
		}
	case SubagentOutcomeIncomplete:
		if outcome.NextStep == "" {
			return SubagentOutcome{}, errors.New("incomplete subagent outcome requires a next step")
		}
	default:
		return SubagentOutcome{}, fmt.Errorf("unknown subagent outcome status %q", outcome.Status)
	}
	return outcome, nil
}
