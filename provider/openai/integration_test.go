package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"harness-api/provider"
)

func TestOpenAIProviderStreamsEventsAndAggregatesResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher := w.(http.Flusher)

		chunks := []map[string]any{
			{"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"content": "The answer"},
			}}},
			{"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0,
					"id":    "call_1",
					"type":  "function",
					"function": map[string]any{
						"name": "search",
					},
				}}},
			}}},
			{"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index":    0,
					"function": map[string]any{"arguments": `{"query":"go`},
				}}},
			}}},
			{"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index":    0,
					"function": map[string]any{"arguments": `lang"}`},
				}}},
			}}},
			{"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "tool_calls",
			}}},
		}

		for i, chunk := range chunks {
			data, _ := json.Marshal(map[string]any{
				"id":      fmt.Sprintf("chunk-%d", i),
				"object":  "chat.completion.chunk",
				"created": 1,
				"model":   "gpt-test",
				"choices": chunk["choices"],
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := NewProvider(Config{URL: server.URL, APIKey: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := provider.StartStream(ctx, p, Request{
		Model:    "gpt-test",
		Messages: []Message{{Role: "user", Content: "search"}},
	})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}

	var events []provider.StreamEvent
	for event := range stream.Subscribe(ctx) {
		events = append(events, event)
	}

	result, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if result.Content != "The answer" || len(result.CompletedTools) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.CompletedTools[0].Arguments["query"] != "golang" {
		t.Fatalf("tool arguments = %#v", result.CompletedTools[0].Arguments)
	}
	if events[len(events)-1].Type != provider.StreamCompleted {
		t.Fatalf("last event = %#v", events[len(events)-1])
	}
}
