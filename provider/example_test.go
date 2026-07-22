package provider_test

import (
	"context"

	sdkopenai "github.com/sashabaranov/go-openai"
	"harness-api/provider"
	"harness-api/provider/openai"
)

// ExampleStartStream verifies the public wiring without making a network
// request. The integration tests exercise the actual stream lifecycle.
func ExampleStartStream() {
	p := openai.NewProvider(openai.Config{
		URL:    "https://api.openai.com/v1",
		APIKey: "sk-example",
	})

	start := provider.StartStream[openai.Request, sdkopenai.ChatCompletionStreamResponse]
	_ = context.Background()
	_ = p
	_ = start
}
