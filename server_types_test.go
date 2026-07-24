package agentcli

import (
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
)

func TestMessageResponseKeepsReasoningSeparate(t *testing.T) {
	response := newMessageResponse(agentruntime.Message{
		Type:      agentruntime.MessageTypeAssistant,
		Content:   "answer",
		Reasoning: "considering",
	})
	if response.Content != "answer" || response.Reasoning != "considering" {
		t.Fatalf("message response = %#v", response)
	}
}

func TestMessageResponseKeepsCompactionCheckpointOpaque(t *testing.T) {
	response := newMessageResponse(agentruntime.Message{
		ID:        "checkpoint-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      agentruntime.MessageTypeCompactionCheckpoint,
		CompactionCheckpoint: &storage.CompactionCheckpoint{
			Summary:                "private cumulative summary",
			CoversThroughMessageID: "message-10",
			TailStartMessageID:     "message-11",
		},
	})
	if response.Type != agentruntime.MessageTypeCompactionCheckpoint {
		t.Fatalf("type = %q, want compaction checkpoint", response.Type)
	}
	if response.Content != "" || response.Reasoning != "" || response.ToolResult != nil || len(response.ToolCalls) != 0 {
		t.Fatalf("checkpoint response leaked internal payload: %#v", response)
	}
}
