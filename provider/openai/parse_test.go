package openai

import (
	"strings"
	"testing"

	openaiclient "github.com/sashabaranov/go-openai"
)

func TestParseEmitsContentAndReasoningAndCompletionEvents(t *testing.T) {
	chunk := openaiclient.ChatCompletionStreamResponse{
		Choices: []openaiclient.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openaiclient.ChatCompletionStreamChoiceDelta{
					Content:          "Hello",
					ReasoningContent: "Thinking...",
				},
				FinishReason: openaiclient.FinishReasonStop,
			},
		},
	}

	events, err := Parse(chunk)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}

	if events[0].Type != "content_received" || events[0].Content != "Hello" {
		t.Fatalf("events[0]=%#v, want content", events[0])
	}
	if events[1].Type != "reasoning_received" || events[1].Reasoning != "Thinking..." {
		t.Fatalf("events[1]=%#v, want reasoning", events[1])
	}
	if events[2].Type != "stream_completed" || events[2].FinishReason != string(openaiclient.FinishReasonStop) {
		t.Fatalf("events[2]=%#v, want stream_completed stop", events[2])
	}
}

func TestParseEmitsModernToolMetadataAndArgumentFragments(t *testing.T) {
	firstArgs := "{\"query\":\"weather in tok"
	secondArgs := "yo\"}"

	index := 0
	chunk := openaiclient.ChatCompletionStreamResponse{
		Choices: []openaiclient.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openaiclient.ChatCompletionStreamChoiceDelta{
					ToolCalls: []openaiclient.ToolCall{
						{
							Index: &index,
							ID:    "call_123",
							Type:  openaiclient.ToolTypeFunction,
							Function: openaiclient.FunctionCall{
								Name:      "search_weather",
								Arguments: firstArgs,
							},
						},
						{
							Index: &index,
							ID:    "call_123",
							Type:  openaiclient.ToolTypeFunction,
							Function: openaiclient.FunctionCall{
								Arguments: secondArgs,
							},
						},
					},
				},
			},
		},
	}

	events, err := Parse(chunk)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}

	if events[0].Type != "tool_call_started" {
		t.Fatalf("events[0].Type = %q, want %q", events[0].Type, "tool_call_started")
	}
	if events[0].Tool == nil || events[0].Tool.Index != 0 || events[0].Tool.ID != "call_123" {
		t.Fatalf("events[0].Tool = %#v, want index 0 id call_123", events[0].Tool)
	}
	if events[0].Tool.Type != "function" {
		t.Fatalf("events[0].Tool.Type = %q, want %q", events[0].Tool.Type, "function")
	}
	if events[0].Tool.Name != "search_weather" {
		t.Fatalf("events[0].Tool.Name = %q, want %q", events[0].Tool.Name, "search_weather")
	}

	if events[1].Type != "tool_arguments_received" || events[1].Tool == nil || events[1].Tool.Arguments != firstArgs {
		t.Fatalf("events[1]=%#v, want first argument fragment", events[1])
	}
	if events[2].Type != "tool_arguments_received" || events[2].Tool == nil || events[2].Tool.Arguments != secondArgs {
		t.Fatalf("events[2]=%#v, want second argument fragment", events[2])
	}
}

func TestParseSkipsLegacyFunctionCallField(t *testing.T) {
	chunk := openaiclient.ChatCompletionStreamResponse{
		Choices: []openaiclient.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openaiclient.ChatCompletionStreamChoiceDelta{
					Content: "plain text",
					FunctionCall: &openaiclient.FunctionCall{
						Name:      "legacy",
						Arguments: "{\"foo\":\"bar\"}",
					},
				},
			},
		},
	}

	events, err := Parse(chunk)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (content only)", len(events))
	}
	if events[0].Type != "content_received" || events[0].Content != "plain text" {
		t.Fatalf("events[0]=%#v, want content event only", events[0])
	}
}

func TestParseReturnsNoEventsForEmptyChoices(t *testing.T) {
	chunk := openaiclient.ChatCompletionStreamResponse{}

	events, err := Parse(chunk)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("len(events) = %d, want 0", len(events))
	}
}

func TestParseToolArgumentsCanContainAnyUnicodeAndBraces(t *testing.T) {
	raw := "{\"query\":\"Tokyo\",\"note\":\"{keep}\"}"
	index := 0
	chunk := openaiclient.ChatCompletionStreamResponse{
		Choices: []openaiclient.ChatCompletionStreamChoice{
			{
				Delta: openaiclient.ChatCompletionStreamChoiceDelta{
					ToolCalls: []openaiclient.ToolCall{
						{
							Index: &index,
							ID:    "call_unicode",
							Type:  openaiclient.ToolTypeFunction,
							Function: openaiclient.FunctionCall{
								Name:      "lookup",
								Arguments: raw,
							},
						},
					},
				},
			},
		},
	}

	events, err := Parse(chunk)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[1].Tool == nil || events[1].Tool.Arguments != raw {
		t.Fatalf("events[1].Tool.Arguments = %q, want %q", events[1].Tool.Arguments, raw)
	}

	if !strings.HasPrefix(events[1].Tool.Arguments, "{\"query\"") {
		t.Fatalf("events[1].Tool.Arguments unexpectedly malformed: %q", events[1].Tool.Arguments)
	}
}
