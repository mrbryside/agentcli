package openai

import sdkopenai "github.com/sashabaranov/go-openai"

// Tool aliases the go-openai tool schema used by the OpenAI provider config.
type Tool = sdkopenai.Tool

type ToolType = sdkopenai.ToolType

const ToolTypeFunction = sdkopenai.ToolTypeFunction

type FunctionDefinition = sdkopenai.FunctionDefinition

type ToolChoice = sdkopenai.ToolChoice

// Request contains the provider-facing OpenAI chat request.
type Request struct {
	Model    string
	Messages []Message
	// ToolSchema optionally replaces the provider's configured tools for this
	// request. A nil slice keeps the configured default; a non-nil empty slice
	// deliberately disables it.
	ToolSchema []Tool
	// ToolChoice optionally controls whether the model may choose a tool or
	// must call a specific tool. It is provider-facing because go-openai uses
	// an interface to support string modes and structured choices.
	ToolChoice  any
	MaxTokens   int
	Temperature float32
}

// Message is the provider-facing chat message shape.
type Message struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []MessageToolCall
}

// MessageToolCall is an already-completed tool call included in history.
type MessageToolCall struct {
	ID        string
	Type      string
	Name      string
	Arguments map[string]any
}
