// Package openai adapts AgentRuntime's provider-neutral model port to the
// existing OpenAI streaming provider.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"
	provideropenai "github.com/mrbryside/agentcli/provider/openai"

	sdkopenai "github.com/sashabaranov/go-openai"
)

// Config selects the OpenAI model and optional request settings.
type Config struct {
	Model       string
	MaxTokens   int
	Temperature float32
}

// Adapter translates generic transcript values only at the OpenAI boundary.
type Adapter struct {
	provider provider.Provider[provideropenai.Request, sdkopenai.ChatCompletionStreamResponse]
	config   Config
}

// New constructs an OpenAI-backed AgentRuntime model.
func New(
	p provider.Provider[provideropenai.Request, sdkopenai.ChatCompletionStreamResponse],
	config Config,
) *Adapter {
	return &Adapter{provider: p, config: config}
}

// Start validates and transforms a generic model request, then starts the
// existing OpenAI provider stream. Session and turn identifiers remain in the
// runtime layer and are intentionally not represented in the SDK request.
func (a *Adapter) Start(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	if a == nil {
		return nil, fmt.Errorf("OpenAI model adapter is nil")
	}
	if a.config.Model == "" {
		return nil, fmt.Errorf("OpenAI model is required")
	}
	if a.provider == nil {
		return nil, fmt.Errorf("OpenAI provider is required")
	}

	messages, err := transformMessages(request.Messages)
	if err != nil {
		return nil, err
	}
	systemMessages := make([]provideropenai.Message, 0, len(request.SystemPrompts)+len(request.ContextReminders))
	for _, prompt := range request.SystemPrompts {
		if strings.TrimSpace(prompt) != "" {
			systemMessages = append(systemMessages, provideropenai.Message{Role: "system", Content: prompt})
		}
	}
	if len(request.ContextReminders) != 0 {
		if !appendContextRemindersToLatestUser(messages, request.ContextReminders) {
			for _, reminder := range request.ContextReminders {
				systemMessages = append(systemMessages, provideropenai.Message{Role: "system", Content: formatContextReminder(reminder)})
			}
		}
	}
	if len(systemMessages) != 0 {
		messages = append(systemMessages, messages...)
	}
	tools, err := transformTools(request.Tools)
	if err != nil {
		return nil, err
	}

	stream, err := provider.StartStream(ctx, a.provider, provideropenai.Request{
		Model:       a.config.Model,
		Messages:    messages,
		ToolSchema:  tools,
		MaxTokens:   a.config.MaxTokens,
		Temperature: a.config.Temperature,
	})
	if err != nil {
		return nil, fmt.Errorf("start OpenAI stream: %w", err)
	}
	return stream, nil
}

// appendContextRemindersToLatestUser keeps all tool-call/result messages in
// their original order and changes only the last user message in the
// provider-owned converted transcript.
func appendContextRemindersToLatestUser(messages []provideropenai.Message, reminders []agentruntime.ContextReminder) bool {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "user" {
			continue
		}
		for _, reminder := range reminders {
			messages[index].Content += "\n\n" + formatContextReminder(reminder)
		}
		return true
	}
	return false
}

func formatContextReminder(reminder agentruntime.ContextReminder) string {
	return "<system-reminder>\n" + reminder.Content + "\n</system-reminder>"
}

func transformMessages(messages []agentruntime.Message) ([]provideropenai.Message, error) {
	if messages == nil {
		return nil, nil
	}

	converted := make([]provideropenai.Message, 0, len(messages))
	for index, message := range messages {
		switch message.Type {
		case agentruntime.MessageTypeSystem, agentruntime.MessageTypeUser, agentruntime.MessageTypeAssistant:
			if strings.TrimSpace(message.Content) == "" {
				continue
			}
			converted = append(converted, provideropenai.Message{
				Role:    string(message.Type),
				Content: message.Content,
			})
		case agentruntime.MessageTypeRuntimeEvent:
			if strings.TrimSpace(message.Content) == "" {
				continue
			}
			// OpenAI chat has no runtime-event role. Treat it as new input while
			// retaining its distinct origin in the provider-neutral transcript.
			converted = append(converted, provideropenai.Message{Role: "user", Content: message.Content})
		case agentruntime.MessageTypeToolCall:
			calls, err := transformToolCalls(message.ToolCalls)
			if err != nil {
				return nil, fmt.Errorf("transform message %d tool calls: %w", index, err)
			}
			if len(calls) == 0 {
				return nil, fmt.Errorf("transform message %d: tool-call message has no calls", index)
			}
			converted = append(converted, provideropenai.Message{
				Role:      "assistant",
				Content:   message.Content,
				ToolCalls: calls,
			})
		case agentruntime.MessageTypeToolResult:
			result, err := transformToolResult(message.ToolResult)
			if err != nil {
				return nil, fmt.Errorf("transform message %d tool result: %w", index, err)
			}
			converted = append(converted, result)
		default:
			return nil, fmt.Errorf("transform message %d: unsupported type %q", index, message.Type)
		}
	}
	return converted, nil
}

func transformToolCalls(calls []agentruntime.ToolCall) ([]provideropenai.MessageToolCall, error) {
	converted := make([]provideropenai.MessageToolCall, len(calls))
	for index, call := range calls {
		if call.CallID == "" || call.Name == "" {
			return nil, fmt.Errorf("tool call %d requires call ID and name", index)
		}

		var arguments map[string]any
		if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
			return nil, fmt.Errorf("decode tool call %q arguments: %w", call.Name, err)
		}
		converted[index] = provideropenai.MessageToolCall{
			ID:        call.CallID,
			Type:      string(provideropenai.ToolTypeFunction),
			Name:      call.Name,
			Arguments: arguments,
		}
	}
	return converted, nil
}

func transformToolResult(result *agentruntime.ToolResult) (provideropenai.Message, error) {
	if result == nil {
		return provideropenai.Message{}, fmt.Errorf("tool result is required")
	}
	if result.CallID == "" || result.Name == "" {
		return provideropenai.Message{}, fmt.Errorf("tool result requires call ID and name")
	}

	message := provideropenai.Message{Role: "tool", ToolCallID: result.CallID}
	switch result.Status {
	case agentruntime.ToolResultSucceeded:
		if !json.Valid(result.Output) {
			return provideropenai.Message{}, fmt.Errorf("tool result %q output is invalid JSON", result.CallID)
		}
		message.Content = string(result.Output)
	case agentruntime.ToolResultFailed, agentruntime.ToolResultInterrupted, agentruntime.ToolResultDenied, agentruntime.ToolResultDeclined:
		if result.Error == "" {
			return provideropenai.Message{}, fmt.Errorf("tool result %q requires an error", result.CallID)
		}
		if len(result.Output) != 0 && !json.Valid(result.Output) {
			return provideropenai.Message{}, fmt.Errorf("tool result %q output is invalid JSON", result.CallID)
		}
		content, err := json.Marshal(struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}{Status: string(result.Status), Error: result.Error})
		if err != nil {
			return provideropenai.Message{}, fmt.Errorf("encode tool result %q: %w", result.CallID, err)
		}
		message.Content = string(content)
	default:
		return provideropenai.Message{}, fmt.Errorf("tool result %q has unsupported status %q", result.CallID, result.Status)
	}
	return message, nil
}

func transformTools(definitions []agentruntime.ToolDefinition) ([]provideropenai.Tool, error) {
	if definitions == nil {
		return nil, nil
	}

	tools := make([]provideropenai.Tool, len(definitions))
	for index, definition := range definitions {
		if definition.Name == "" {
			return nil, fmt.Errorf("tool definition %d requires a name", index)
		}
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil || schema == nil {
			if err == nil {
				err = fmt.Errorf("schema must be a JSON object")
			}
			return nil, fmt.Errorf("decode tool definition %q schema: %w", definition.Name, err)
		}

		parameters := make(json.RawMessage, len(definition.InputSchema))
		copy(parameters, definition.InputSchema)
		tools[index] = provideropenai.Tool{
			Type: provideropenai.ToolTypeFunction,
			Function: &provideropenai.FunctionDefinition{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  parameters,
			},
		}
	}
	return tools, nil
}
