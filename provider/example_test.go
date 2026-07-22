package provider_test

import (
	"context"

	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/provider/openai"
	sdkopenai "github.com/sashabaranov/go-openai"
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
