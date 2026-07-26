package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// ExtraBody contains provider-specific top-level JSON fields merged into
	// every chat-completions request after the standard request is encoded.
	ExtraBody map[string]any
	Timeout   time.Duration
}

// Provider adapts go-openai to the generic provider contract.
type Provider struct {
	config        Config
	extraBodyJSON json.RawMessage
	configError   error
}

// NewProvider creates an OpenAI provider with copied tool configuration.
func NewProvider(config Config) generic.Provider[Request, sdkopenai.ChatCompletionStreamResponse] {
	config.ToolSchema = cloneTools(config.ToolSchema)
	var extraBodyJSON json.RawMessage
	var configError error
	if len(config.ExtraBody) != 0 {
		extraBodyJSON, configError = json.Marshal(config.ExtraBody)
	}
	config.ExtraBody = nil
	return Provider{config: config, extraBodyJSON: extraBodyJSON, configError: configError}
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
	if p.configError != nil {
		return nil, fmt.Errorf("encode OpenAI extra body: %w", p.configError)
	}

	sdkRequest, err := toSDKRequest(request, p.config.ToolSchema)
	if err != nil {
		return nil, err
	}

	config := sdkopenai.DefaultConfig(p.config.APIKey)
	if p.config.URL != "" {
		config.BaseURL = p.config.URL
	}
	httpClient := &http.Client{Timeout: p.config.Timeout}
	if len(p.extraBodyJSON) != 0 {
		httpClient.Transport = extraBodyTransport{
			base:      http.DefaultTransport,
			extraBody: p.extraBodyJSON,
		}
	}
	config.HTTPClient = httpClient
	client := sdkopenai.NewClientWithConfig(config)

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

	sdkRequest := sdkopenai.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    messages,
		Tools:       tools,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		Stream:      true,
	}
	if request.Reasoning != nil {
		sdkRequest.ChatTemplateKwargs = map[string]any{
			"enable_thinking": *request.Reasoning,
		}
	}
	return sdkRequest, nil
}

type extraBodyTransport struct {
	base      http.RoundTripper
	extraBody json.RawMessage
}

func (transport extraBodyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.Body == nil {
		return nil, errors.New("OpenAI request body is required")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, fmt.Errorf("read OpenAI request body: %w", err)
	}
	_ = request.Body.Close()

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode OpenAI request body: %w", err)
	}
	var extraBody map[string]json.RawMessage
	if err := json.Unmarshal(transport.extraBody, &extraBody); err != nil {
		return nil, fmt.Errorf("decode OpenAI extra body: %w", err)
	}
	for key, value := range extraBody {
		payload[key] = value
	}
	body, err = json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI request body: %w", err)
	}

	clone := request.Clone(request.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	base := transport.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
