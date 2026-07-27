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
	SubagentOutcomeFailed     SubagentOutcomeStatus = "failed"
)

// SubagentOutcome is the validated report echoed into the generic transcript.
type SubagentOutcome struct {
	Status   SubagentOutcomeStatus `json:"status"`
	Summary  string                `json:"summary"`
	NextStep string                `json:"next_step,omitempty"`
	Error    string                `json:"error,omitempty"`
}

// NewSubagentOutcomeTool returns the child-only structured completion report.
func NewSubagentOutcomeTool() Tool {
	return Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        SubagentOutcomeToolName,
			Description: "Report the child turn's semantic outcome exactly once after domain work and before the final assistant answer. This report creates the authoritative parent callback; it does not replace the child's concise final answer. Use completed only when every required part of the delegated task is resolved; include a concise evidence-grounded summary and omit next_step and error. Use incomplete when required work, information, confirmation, or a decision remains; include a concise summary and one concrete non-empty next_step, and omit error. Use failed only when a terminal error prevents the delegated task from continuing; include a concise summary and the actual non-empty error, omit next_step, and never invent recovery work. If unsure whether work is resolved, report incomplete. After a successful report, do not call this tool or repeat domain work again; write the final answer for the parent.",
			InputSchema: mustRawToolSchema(`{"type":"object","oneOf":[{"type":"object","properties":{"status":{"const":"completed","description":"Every required part of the delegated task is resolved."},"summary":{"type":"string","minLength":1,"description":"Concise evidence-grounded outcome for the parent callback."}},"required":["status","summary"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"incomplete","description":"Required work, information, confirmation, or a decision remains."},"summary":{"type":"string","minLength":1,"description":"Concise evidence-grounded outcome for the parent callback."},"next_step":{"type":"string","minLength":1,"description":"One concrete required next step."}},"required":["status","summary","next_step"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"failed","description":"A terminal error prevents the delegated task from continuing."},"summary":{"type":"string","minLength":1,"description":"Concise evidence-grounded outcome for the parent callback."},"error":{"type":"string","minLength":1,"description":"The actual terminal error; do not invent recovery work."}},"required":["status","summary","error"],"additionalProperties":false}]}`),
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
	outcome.Error = strings.TrimSpace(outcome.Error)
	if outcome.Summary == "" {
		return SubagentOutcome{}, errors.New("subagent outcome summary is required")
	}
	switch outcome.Status {
	case SubagentOutcomeCompleted:
		if outcome.NextStep != "" {
			return SubagentOutcome{}, errors.New("completed subagent outcome cannot require a next step")
		}
		if outcome.Error != "" {
			return SubagentOutcome{}, errors.New("completed subagent outcome cannot contain an error")
		}
	case SubagentOutcomeIncomplete:
		if outcome.NextStep == "" {
			return SubagentOutcome{}, errors.New("incomplete subagent outcome requires a next step")
		}
		if outcome.Error != "" {
			return SubagentOutcome{}, errors.New("incomplete subagent outcome cannot contain an error")
		}
	case SubagentOutcomeFailed:
		if outcome.NextStep != "" {
			return SubagentOutcome{}, errors.New("failed subagent outcome cannot require a next step")
		}
		if outcome.Error == "" {
			return SubagentOutcome{}, errors.New("failed subagent outcome requires an error")
		}
	default:
		return SubagentOutcome{}, fmt.Errorf("unknown subagent outcome status %q", outcome.Status)
	}
	return outcome, nil
}
