package agentcli

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"
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

func TestEventResponsePreservesProviderCompletionPayload(t *testing.T) {
	event := agentruntime.AgentEvent{
		Type: agentruntime.ProviderEventReceived,
		ProviderEvent: provider.StreamEvent{
			Type: provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
				Content:   "authoritative answer",
				Reasoning: "authoritative reasoning",
				CompletedTools: []provider.ToolCall{{
					ID: "call-1", Type: "function", Name: "lookup",
					Arguments: map[string]any{"topic": "agentcli"},
				}},
				Finished: true,
			}},
		},
	}

	response := newEventResponse(event)
	payload := response.ProviderEvent.Payload
	if payload == nil || payload.Result == nil {
		t.Fatalf("provider payload = %#v", payload)
	}
	if payload.Result.Content != "authoritative answer" ||
		payload.Result.Reasoning != "authoritative reasoning" ||
		!payload.Result.Finished ||
		len(payload.Result.CompletedTools) != 1 ||
		payload.Result.CompletedTools[0].ID != "call-1" ||
		!reflect.DeepEqual(payload.Result.CompletedTools[0].Arguments, map[string]any{"topic": "agentcli"}) {
		t.Fatalf("provider payload result = %#v", payload.Result)
	}
}

func TestEventResponsePreservesProviderFailurePayload(t *testing.T) {
	response := newEventResponse(agentruntime.AgentEvent{
		Type: agentruntime.ProviderEventReceived,
		ProviderEvent: provider.StreamEvent{
			Type:    provider.StreamFailed,
			Error:   errors.New("stream failed"),
			Payload: provider.StreamFailedPayload{Error: errors.New("payload failed")},
		},
	})
	if response.ProviderEvent == nil || response.ProviderEvent.Error != "stream failed" ||
		response.ProviderEvent.Payload == nil || response.ProviderEvent.Payload.Error != "payload failed" {
		t.Fatalf("provider failure response = %#v", response.ProviderEvent)
	}
}
