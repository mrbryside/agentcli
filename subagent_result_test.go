package agentcli

import (
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestSubagentResultUsesStoredRuntimeStateAndFinalText(t *testing.T) {
	record := storage.Subagent{
		ID: "subagent", DisplayName: "Fern", DefinitionName: "operator",
		MainAgentSessionID: "mainAgent", MainAgentTurnID: "mainAgent-turn",
		SubagentSessionID: "subagent-session", LastSubagentTurnID: "subagent-turn",
	}
	answer := agentruntime.Message{ID: "answer", TurnID: "subagent-turn", Type: agentruntime.MessageTypeAssistant, Content: "Final answer"}

	t.Run("completed turn retains final answer", func(t *testing.T) {
		record.LastResultStatus = storage.SubagentResultCompleted
		result := subagentResultFromMessages(record, []agentruntime.Message{answer})
		if result.Status != SubagentResultCompleted || result.FinalAnswer == nil || result.FinalAnswer.Content != "Final answer" {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("runtime error is failed", func(t *testing.T) {
		failed := record
		failed.LastResultStatus = storage.SubagentResultFailed
		failed.LastResultError = "Discord API is unavailable."
		result := subagentResultFromMessages(failed, []agentruntime.Message{answer})
		if result.Status != SubagentResultFailed || result.Error != "Discord API is unavailable." || result.FinalAnswer == nil {
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

func TestTaskResultFromFinalOutputUsesContractAndRuntimeState(t *testing.T) {
	definition := SubagentDefinition{
		Name: "operator",
		Result: &AgentResultContract{
			MessageField: "message",
			Metadata: map[string]AgentResultMetadataField{
				"requires_reply": {Type: "boolean", Required: true},
			},
		},
	}
	completed := taskResultFromFinalOutput("task-1", definition, `{"message":"Done","requires_reply":false}`, false)
	if completed.TaskID != "task-1" || completed.AgentName != "operator" || completed.State != TaskStateCompleted || completed.Output != "Done" || completed.Error != "" {
		t.Fatalf("completed task result = %#v", completed)
	}
	incomplete := taskResultFromFinalOutput("task-1", definition, `{"message":"Need more","requires_reply":true}`, true)
	if incomplete.State != TaskStateIncomplete || incomplete.Output != "Need more" {
		t.Fatalf("incomplete task result = %#v", incomplete)
	}
	invalid := taskResultFromFinalOutput("task-1", definition, `{"requires_reply":true}`, false)
	if invalid.State != TaskStateError || !strings.Contains(invalid.Error, "message") {
		t.Fatalf("invalid task result = %#v", invalid)
	}
}
