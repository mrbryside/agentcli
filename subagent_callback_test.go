package agentcli

import (
	"encoding/json"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestSubagentCallbackRequiresExplicitCompletedOutcome(t *testing.T) {
	record := storage.Subagent{
		ID: "child", DisplayName: "Fern", DefinitionName: "operator",
		ParentSessionID: "parent", ParentTurnID: "parent-turn",
		SessionID: "child-session", LastTurnID: "child-turn",
	}
	answer := agentruntime.Message{ID: "answer", TurnID: "child-turn", Type: agentruntime.MessageTypeAssistant, Content: "Final answer"}

	t.Run("missing report defaults incomplete", func(t *testing.T) {
		callback := callbackFromMessages(record, []agentruntime.Message{answer})
		if callback.Status != SubagentCallbackIncomplete || callback.FinalAnswer == nil {
			t.Fatalf("callback = %#v", callback)
		}
	})

	t.Run("completed report is authoritative", func(t *testing.T) {
		output, err := json.Marshal(toolexecution.SubagentOutcome{Status: toolexecution.SubagentOutcomeCompleted, Summary: "Transfer is fully resolved."})
		if err != nil {
			t.Fatal(err)
		}
		result := agentruntime.Message{
			ID: "result", TurnID: "child-turn", Type: agentruntime.MessageTypeToolResult,
			ToolResult: &agentruntime.ToolResult{CallID: "outcome", Name: toolexecution.SubagentOutcomeToolName, Status: agentruntime.ToolResultSucceeded, Output: output},
		}
		callback := callbackFromMessages(record, []agentruntime.Message{result, answer})
		if callback.Status != SubagentCallbackCompleted || callback.Summary != "Transfer is fully resolved." || callback.NextStep != "" {
			t.Fatalf("callback = %#v", callback)
		}
	})

	t.Run("incomplete report carries next step", func(t *testing.T) {
		output, err := json.Marshal(toolexecution.SubagentOutcome{Status: toolexecution.SubagentOutcomeIncomplete, Summary: "Recipient is ambiguous.", NextStep: "Ask which account to use."})
		if err != nil {
			t.Fatal(err)
		}
		result := agentruntime.Message{
			ID: "result", TurnID: "child-turn", Type: agentruntime.MessageTypeToolResult,
			ToolResult: &agentruntime.ToolResult{CallID: "outcome", Name: toolexecution.SubagentOutcomeToolName, Status: agentruntime.ToolResultSucceeded, Output: output},
		}
		callback := callbackFromMessages(record, []agentruntime.Message{result, answer})
		if callback.Status != SubagentCallbackIncomplete || callback.Summary != "Recipient is ambiguous." || callback.NextStep != "Ask which account to use." {
			t.Fatalf("callback = %#v", callback)
		}
	})
}
