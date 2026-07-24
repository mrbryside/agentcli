package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	generic "github.com/mrbryside/agentcli/provider"
	sdkopenai "github.com/sashabaranov/go-openai"
)

// Config contains immutable OpenAI provider configuration.
type Config struct {
	URL        string
	APIKey     string
	ToolSchema []Tool
	Timeout    time.Duration
}

// Provider adapts go-openai to the generic provider contract.
type Provider struct {
	config Config
}

// NewProvider creates an OpenAI provider with copied tool configuration.
func NewProvider(config Config) generic.Provider[Request, sdkopenai.ChatCompletionStreamResponse] {
	config.ToolSchema = cloneTools(config.ToolSchema)
	return Provider{config: config}
}

func cloneTools(tools []Tool) []Tool {
	if tools == nil {
		return nil
	}
	clone := make([]Tool, len(tools))
	for i, tool := range tools {
		clone[i] = tool
		if tool.Function != nil {
			function := *tool.Function
			function.Parameters = cloneToolParameters(tool.Function.Parameters)
			clone[i].Function = &function
		}
	}
	return clone
}

func cloneToolParameters(parameters any) any {
	switch value := parameters.(type) {
	case json.RawMessage:
		clone := make(json.RawMessage, len(value))
		copy(clone, value)
		return clone
	case []byte:
		clone := make([]byte, len(value))
		copy(clone, value)
		return clone
	case map[string]any:
		clone := make(map[string]any, len(value))
		for key, item := range value {
			clone[key] = cloneToolParameters(item)
		}
		return clone
	case []any:
		clone := make([]any, len(value))
		for i, item := range value {
			clone[i] = cloneToolParameters(item)
		}
		return clone
	default:
		return value
	}
}

// Stream creates a go-openai streaming chat completion.
func (p Provider) Stream(ctx context.Context, request Request) (generic.ChunkStream[sdkopenai.ChatCompletionStreamResponse], error) {
	if p.config.APIKey == "" {
		return nil, fmt.Errorf("openai API key is required")
	}

	config := sdkopenai.DefaultConfig(p.config.APIKey)
	if p.config.URL != "" {
		config.BaseURL = p.config.URL
	}
	if p.config.Timeout > 0 {
		config.HTTPClient = &http.Client{Timeout: p.config.Timeout}
	}
	client := sdkopenai.NewClientWithConfig(config)

	sdkRequest, err := toSDKRequest(request, p.config.ToolSchema)
	if err != nil {
		return nil, err
	}
	stream, err := client.CreateChatCompletionStream(ctx, sdkRequest)
	if err != nil {
		return nil, fmt.Errorf("create OpenAI chat stream: %w", err)
	}
	return chatCompletionStream{stream: stream}, nil
}

// Parse converts an OpenAI chunk into generic provider events.
func (Provider) Parse(chunk sdkopenai.ChatCompletionStreamResponse) ([]generic.StreamEvent, error) {
	return Parse(chunk)
}

func toSDKRequest(request Request, configuredTools []Tool) (sdkopenai.ChatCompletionRequest, error) {
	tools := configuredTools
	if request.ToolSchema != nil {
		tools = request.ToolSchema
	}
	tools = cloneTools(tools)

	messages := make([]sdkopenai.ChatCompletionMessage, len(request.Messages))
	for i, message := range request.Messages {
		messages[i] = sdkopenai.ChatCompletionMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		}
		if len(message.ToolCalls) == 0 {
			continue
		}

		calls := make([]sdkopenai.ToolCall, len(message.ToolCalls))
		for j, call := range message.ToolCalls {
			arguments, err := json.Marshal(call.Arguments)
			if err != nil {
				return sdkopenai.ChatCompletionRequest{}, fmt.Errorf("marshal tool call %q arguments: %w", call.Name, err)
			}
			calls[j] = sdkopenai.ToolCall{
				ID:   call.ID,
				Type: sdkopenai.ToolType(call.Type),
				Function: sdkopenai.FunctionCall{
					Name:      call.Name,
					Arguments: string(arguments),
				},
			}
		}
		messages[i].ToolCalls = calls
	}

	return sdkopenai.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    messages,
		Tools:       tools,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		Stream:      true,
	}, nil
}
