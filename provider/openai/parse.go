package openai

import (
	openaiclient "github.com/sashabaranov/go-openai"

	"github.com/mrbryside/agentcli/provider"
)

// Parse converts one OpenAI streaming chunk into provider-neutral events.
func Parse(chunk openaiclient.ChatCompletionStreamResponse) ([]provider.StreamEvent, error) {
	events := make([]provider.StreamEvent, 0)

	// Track tool-call indexes we have already emitted a start event for,
	// avoiding duplicate metadata when a single chunk contains
	// multiple fragments for the same tool call.
	startedToolIndexes := map[int]struct{}{}

	var finishReason string

	for _, choice := range chunk.Choices {
		delta := choice.Delta

		if delta.Content != "" {
			events = append(events, provider.StreamEvent{
				Type:    provider.ContentReceived,
				Content: delta.Content,
			})
		}

		if delta.ReasoningContent != "" {
			events = append(events, provider.StreamEvent{
				Type:      provider.ReasoningReceived,
				Reasoning: delta.ReasoningContent,
			})
		}

		for _, toolCall := range delta.ToolCalls {
			streamToolIndex := choice.Index
			if toolCall.Index != nil {
				streamToolIndex = *toolCall.Index
			}

			if _, seen := startedToolIndexes[streamToolIndex]; !seen {
				if toolCall.ID != "" || toolCall.Type != "" || toolCall.Function.Name != "" {
					events = append(events, provider.StreamEvent{
						Type: provider.ToolCallStarted,
						Tool: &provider.ToolEvent{
							Index: streamToolIndex,
							ID:    toolCall.ID,
							Type:  string(toolCall.Type),
							Name:  toolCall.Function.Name,
						},
					})
					startedToolIndexes[streamToolIndex] = struct{}{}
				}
			}

			if toolCall.Function.Arguments != "" {
				events = append(events, provider.StreamEvent{
					Type: provider.ToolArgumentsReceived,
					Tool: &provider.ToolEvent{
						Index:     streamToolIndex,
						ID:        toolCall.ID,
						Type:      string(toolCall.Type),
						Name:      toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
					},
				})
			}
		}

		if finishReason == "" && choice.FinishReason != "" {
			finishReason = string(choice.FinishReason)
		}
	}

	if finishReason != "" {
		events = append(events, provider.StreamEvent{
			Type:         provider.StreamCompleted,
			FinishReason: finishReason,
		})
	}

	return events, nil
}
