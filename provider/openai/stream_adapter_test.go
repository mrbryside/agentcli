package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatCompletionStreamAdapterDelegatesRecvAndClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewProvider(Config{URL: server.URL, APIKey: "secret"})
	stream, err := p.Stream(context.Background(), Request{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if err := stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Close: %v", err)
	}
}
