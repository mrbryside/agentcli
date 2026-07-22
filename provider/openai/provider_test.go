package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestProviderTimeoutStopsStalledStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	provider := NewProvider(Config{URL: server.URL, APIKey: "secret", Timeout: 50 * time.Millisecond})
	stream, err := provider.Stream(context.Background(), Request{Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := stream.Recv(); err == nil {
		t.Fatal("stalled stream did not time out")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestNewProviderUsesOpenAIConfigAndRequest(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	tools := []Tool{{
		Type: ToolTypeFunction,
		Function: &FunctionDefinition{
			Name:        "search",
			Description: "Search the web",
		},
	}}
	p := NewProvider(Config{URL: server.URL, APIKey: "secret", ToolSchema: tools})
	tools[0].Function.Name = "mutated-after-construction"

	stream, err := p.Stream(context.Background(), Request{
		Model:       "gpt-test",
		MaxTokens:   200,
		Temperature: 0.25,
		Messages: []Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil && err != io.EOF {
		t.Fatalf("Recv: %v", err)
	}

	if body["model"] != "gpt-test" || body["stream"] != true {
		t.Fatalf("request body = %#v", body)
	}
	if body["max_tokens"] != float64(200) || body["temperature"] != 0.25 {
		t.Fatalf("request options = %#v", body)
	}
	requestMessages := body["messages"].([]any)
	if requestMessages[0].(map[string]any)["content"] != "hello" {
		t.Fatalf("messages = %#v", requestMessages)
	}
	requestTools := body["tools"].([]any)
	toolFunction := requestTools[0].(map[string]any)["function"].(map[string]any)
	if toolFunction["name"] != "search" {
		t.Fatalf("tools = %#v", requestTools)
	}
}

func TestNewProviderRequiresAPIKey(t *testing.T) {
	p := NewProvider(Config{})
	_, err := p.Stream(context.Background(), Request{Model: "gpt-test"})
	if err == nil {
		t.Fatal("expected missing API key error")
	}
}

func TestRequestToolSchemaOverridesConfiguredTools(t *testing.T) {
	var names []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		for _, tool := range body.Tools {
			names = append(names, tool.Function.Name)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewProvider(Config{
		URL:        server.URL,
		APIKey:     "secret",
		ToolSchema: []Tool{functionTool("configured")},
	})
	stream, err := p.Stream(context.Background(), Request{
		Model:      "gpt-test",
		ToolSchema: []Tool{functionTool("request")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil && err != io.EOF {
		t.Fatalf("Recv: %v", err)
	}

	if got, want := names, []string{"request"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool names = %#v, want %#v", got, want)
	}
}

func TestRequestNilToolSchemaUsesConfiguredTools(t *testing.T) {
	var names []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		for _, tool := range body.Tools {
			names = append(names, tool.Function.Name)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewProvider(Config{
		URL:        server.URL,
		APIKey:     "secret",
		ToolSchema: []Tool{functionTool("configured")},
	})
	stream, err := p.Stream(context.Background(), Request{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil && err != io.EOF {
		t.Fatalf("Recv: %v", err)
	}

	if got, want := names, []string{"configured"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool names = %#v, want %#v", got, want)
	}
}

func functionTool(name string) Tool {
	return Tool{
		Type:     ToolTypeFunction,
		Function: &FunctionDefinition{Name: name},
	}
}
