package agentcli

import (
	"context"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
)

func TestCallbackDeliveryCompletionGuardRepairsOnlySilentCallbackTurns(t *testing.T) {
	callback := SubagentCallback{
		SubagentID: "subagent-1", DisplayName: "Lark", Status: SubagentCallbackCompleted,
		Summary: "Research completed.",
	}.RuntimeMessage()
	callback.SessionID = "parent"
	callback.TurnID = "callback-turn"

	tests := []struct {
		name        string
		messages    []agentruntime.Message
		repairCount int
		wantAction  agentruntime.CompletionAction
	}{
		{
			name: "ordinary turn proceeds",
			messages: []agentruntime.Message{{
				SessionID: "parent", TurnID: "callback-turn", Type: agentruntime.MessageTypeUser, Content: "hello",
			}},
			wantAction: agentruntime.CompletionProceed,
		},
		{
			name:       "silent callback turn repairs",
			messages:   []agentruntime.Message{callback, {SessionID: "parent", TurnID: "callback-turn", Type: agentruntime.MessageTypeToolCall}},
			wantAction: agentruntime.CompletionRetry,
		},
		{
			name: "content alongside close is already delivered",
			messages: []agentruntime.Message{callback, {
				SessionID: "parent", TurnID: "callback-turn", Type: agentruntime.MessageTypeToolCall,
				Content: "Lark completed the research successfully.",
			}},
			wantAction: agentruntime.CompletionProceed,
		},
		{
			name: "final assistant answer is delivered",
			messages: []agentruntime.Message{callback, {
				SessionID: "parent", TurnID: "callback-turn", Type: agentruntime.MessageTypeAssistant,
				Content: "Lark completed the research successfully.",
			}},
			wantAction: agentruntime.CompletionProceed,
		},
		{
			name:        "repair is bounded",
			messages:    []agentruntime.Message{callback},
			repairCount: 1,
			wantAction:  agentruntime.CompletionProceed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := callbackDeliveryCompletionGuard(context.Background(), agentruntime.CompletionAttempt{
				SessionID: "parent", TurnID: "callback-turn", Messages: test.messages, RepairCount: test.repairCount,
			})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Action != test.wantAction {
				t.Fatalf("action = %q, want %q", decision.Action, test.wantAction)
			}
			if test.wantAction == agentruntime.CompletionRetry {
				if decision.ToolAllowlist == nil || len(decision.ToolAllowlist) != 0 {
					t.Fatalf("repair tool allowlist = %#v, want explicit empty allowlist", decision.ToolAllowlist)
				}
				if len(decision.ContextReminders) != 1 {
					t.Fatalf("repair reminders = %#v", decision.ContextReminders)
				}
			}
		})
	}
}

func TestCallbackDeliveryCompletionGuardIgnoresMalformedRuntimeMessages(t *testing.T) {
	decision, err := callbackDeliveryCompletionGuard(context.Background(), agentruntime.CompletionAttempt{
		SessionID: "parent", TurnID: "turn",
		Messages: []agentruntime.Message{{
			SessionID: "parent", TurnID: "turn", Type: agentruntime.MessageTypeRuntimeEvent,
			Content: "<subagent_callback>{not-json}</subagent_callback>",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != agentruntime.CompletionProceed {
		t.Fatalf("action = %q, want proceed", decision.Action)
	}
}
