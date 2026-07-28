package agentcli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestSubagentResultRequiresExplicitCompletedOutcome(t *testing.T) {
	record := storage.Subagent{
		ID: "subagent", DisplayName: "Fern", DefinitionName: "operator",
		MainAgentSessionID: "mainAgent", MainAgentTurnID: "mainAgent-turn",
		SubagentSessionID: "subagent-session", LastSubagentTurnID: "subagent-turn",
	}
	answer := agentruntime.Message{ID: "answer", TurnID: "subagent-turn", Type: agentruntime.MessageTypeAssistant, Content: "Final answer"}

	t.Run("missing report defaults incomplete", func(t *testing.T) {
		result := subagentResultFromMessages(record, []agentruntime.Message{answer})
		if result.Status != SubagentResultIncomplete || result.FinalAnswer == nil {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("completed report is authoritative", func(t *testing.T) {
		output, err := json.Marshal(toolexecution.SubagentReport{Status: toolexecution.SubagentReportCompleted, Summary: "Transfer is fully resolved."})
		if err != nil {
			t.Fatal(err)
		}
		report := agentruntime.Message{
			ID: "result", TurnID: "subagent-turn", Type: agentruntime.MessageTypeToolResult,
			ToolResult: &agentruntime.ToolResult{CallID: "report", Name: toolexecution.SubagentResultToolName, Status: agentruntime.ToolResultSucceeded, Output: output},
		}
		result := subagentResultFromMessages(record, []agentruntime.Message{report, answer})
		if result.Status != SubagentResultCompleted || result.Summary != "Transfer is fully resolved." || result.NextStep != "" {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("incomplete report carries next step", func(t *testing.T) {
		output, err := json.Marshal(toolexecution.SubagentReport{Status: toolexecution.SubagentReportIncomplete, Summary: "Recipient is ambiguous.", NextStep: "Ask which account to use."})
		if err != nil {
			t.Fatal(err)
		}
		report := agentruntime.Message{
			ID: "result", TurnID: "subagent-turn", Type: agentruntime.MessageTypeToolResult,
			ToolResult: &agentruntime.ToolResult{CallID: "report", Name: toolexecution.SubagentResultToolName, Status: agentruntime.ToolResultSucceeded, Output: output},
		}
		result := subagentResultFromMessages(record, []agentruntime.Message{report, answer})
		if result.Status != SubagentResultIncomplete || result.Summary != "Recipient is ambiguous." || result.NextStep != "Ask which account to use." {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("failed report carries actual error without next step", func(t *testing.T) {
		output, err := json.Marshal(toolexecution.SubagentReport{
			Status:  toolexecution.SubagentReportFailed,
			Summary: "The operation cannot continue.",
			Error:   "Discord API is unavailable.",
		})
		if err != nil {
			t.Fatal(err)
		}
		report := agentruntime.Message{
			ID: "result", TurnID: "subagent-turn", Type: agentruntime.MessageTypeToolResult,
			ToolResult: &agentruntime.ToolResult{CallID: "report", Name: toolexecution.SubagentResultToolName, Status: agentruntime.ToolResultSucceeded, Output: output},
		}
		result := subagentResultFromMessages(record, []agentruntime.Message{report, answer})
		if result.Status != SubagentResultFailed || result.Summary != "The operation cannot continue." ||
			result.Error != "Discord API is unavailable." || result.NextStep != "" {
			t.Fatalf("result = %#v", result)
		}
	})
}

func TestSubagentResultRuntimeMessageIncludesResultProgress(t *testing.T) {
	result := SubagentResult{
		SubagentID: "subagent-a", DefinitionName: "web-summary", DisplayName: "Vale",
		SubagentTurnID: "turn-a", Status: SubagentResultCompleted, Summary: "Done.",
	}
	message := result.RuntimeMessage(toolexecution.ResponseScopeResultProgress{
		PendingCount:        1,
		AllResultsDelivered: false,
		PendingResults: []toolexecution.ResponseScopePendingResult{{
			SubagentID: "subagent-b", DefinitionName: "web-summary", DisplayName: "Luna",
			AssignmentID: "assignment-b",
		}},
		DeliveredResults: []toolexecution.ResponseScopeDeliveredResult{{
			SubagentID: "subagent-a", DefinitionName: "web-summary", DisplayName: "Vale",
			AssignmentID: "assignment-a", SubagentTurnID: "turn-a", ResultStatus: "completed",
		}},
	})
	for _, expected := range []string{
		`"result_progress"`,
		`"pending_count":1`,
		`"subagent_turn_id":"turn-a"`,
		`"pending_results":[{"subagent_id":"subagent-b"`,
		`"delivered_results":[{"subagent_id":"subagent-a"`,
		`"result_status":"completed"`,
		`"status":"completed"`,
		"Read result_progress first",
		"process each once",
		"never duplicate, replace, retry, or poll it",
		"More results arrive automatically",
	} {
		if !strings.Contains(message.Content, expected) {
			t.Fatalf("result runtime message missing %q: %s", expected, message.Content)
		}
	}
}
