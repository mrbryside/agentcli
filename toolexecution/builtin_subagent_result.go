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

// SubagentResultToolName is a subagent-only framework tool. It is deliberately
// separate from the main agent management-tool catalog.
const SubagentResultToolName = "report_subagent_result"

// SubagentReportStatus is the subagent's explicit semantic assessment of its
// delegated task, independent of the running/idle/closed lifecycle.
type SubagentReportStatus string

const (
	SubagentReportCompleted  SubagentReportStatus = "completed"
	SubagentReportIncomplete SubagentReportStatus = "incomplete"
	SubagentReportFailed     SubagentReportStatus = "failed"
)

// SubagentReport is the validated report echoed into the generic transcript.
type SubagentReport struct {
	Status   SubagentReportStatus `json:"status"`
	Summary  string               `json:"summary"`
	NextStep string               `json:"next_step,omitempty"`
	Error    string               `json:"error,omitempty"`
}

// NewSubagentReportTool returns the subagent-only structured completion report.
func NewSubagentReportTool() Tool {
	return Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        SubagentResultToolName,
			Description: "Report this subagent's result exactly once after the assigned work is finished and before the final assistant answer. Use status=completed only when every required part is resolved; include summary and omit next_step and error. Use status=incomplete when work, information, confirmation, or a decision remains; include summary and one concrete next_step, and omit error. Use status=failed only when a terminal error prevents continuation; include summary and the actual error, and omit next_step. If unsure, use incomplete. After this tool succeeds, do not call it or repeat domain work; write one concise final answer for the main agent.",
			// Keep the provider-facing schema flat and limited to broadly
			// supported function-calling keywords. Some OpenAI-compatible
			// models ignore properties nested under root-level combinators and
			// consequently emit {}. ParseSubagentReport remains the
			// authoritative validator for status-dependent field rules.
			InputSchema: mustRawToolSchema(`{"type":"object","properties":{"status":{"type":"string","enum":["completed","incomplete","failed"],"description":"completed when all assigned work is resolved; incomplete when one concrete next step remains; failed when a terminal error prevents continuation."},"summary":{"type":"string","minLength":1,"description":"Concise result for the main agent."},"next_step":{"type":"string","minLength":1,"description":"Required only for incomplete: one concrete next step."},"error":{"type":"string","minLength":1,"description":"Required only for failed: the actual terminal error."}},"required":["status","summary"],"additionalProperties":false}`),
		},
		Handler: func(_ context.Context, arguments json.RawMessage) (json.RawMessage, error) {
			report, err := ParseSubagentReport(arguments)
			if err != nil {
				return nil, err
			}
			return json.Marshal(report)
		},
	}
}

// ParseSubagentReport validates the subagent report from tool arguments or a
// successful tool result. Unknown or ambiguous values are never completed.
func ParseSubagentReport(value json.RawMessage) (SubagentReport, error) {
	var report SubagentReport
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return SubagentReport{}, fmt.Errorf("decode subagent report: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return SubagentReport{}, errors.New("decode subagent report: multiple JSON values")
		}
		return SubagentReport{}, fmt.Errorf("decode subagent report: %w", err)
	}
	report.Summary = strings.TrimSpace(report.Summary)
	report.NextStep = strings.TrimSpace(report.NextStep)
	report.Error = strings.TrimSpace(report.Error)
	if report.Summary == "" {
		return SubagentReport{}, errors.New("subagent report summary is required")
	}
	switch report.Status {
	case SubagentReportCompleted:
		if report.NextStep != "" {
			return SubagentReport{}, errors.New("completed subagent report cannot require a next step")
		}
		if report.Error != "" {
			return SubagentReport{}, errors.New("completed subagent report cannot contain an error")
		}
	case SubagentReportIncomplete:
		if report.NextStep == "" {
			return SubagentReport{}, errors.New("incomplete subagent report requires a next step")
		}
		if report.Error != "" {
			return SubagentReport{}, errors.New("incomplete subagent report cannot contain an error")
		}
	case SubagentReportFailed:
		if report.NextStep != "" {
			return SubagentReport{}, errors.New("failed subagent report cannot require a next step")
		}
		if report.Error == "" {
			return SubagentReport{}, errors.New("failed subagent report requires an error")
		}
	default:
		return SubagentReport{}, fmt.Errorf("unknown subagent report status %q", report.Status)
	}
	return report, nil
}
