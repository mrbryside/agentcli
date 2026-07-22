package agentcli

import (
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
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
