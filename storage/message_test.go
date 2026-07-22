package storage

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestValidateMessage(t *testing.T) {
	validMessage := func(messageType MessageType) Message {
		return Message{
			ID:        "msg_1",
			SessionID: "session_1",
			TurnID:    "turn_1",
			Type:      messageType,
			CreatedAt: time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC),
		}
	}

	content := validMessage(MessageTypeUser)
	content.Content = "hello"

	toolCalls := validMessage(MessageTypeToolCall)
	toolCalls.Content = "I'll look that up."
	toolCalls.ToolCalls = []ToolCall{{
		CallID:    "call_1",
		Name:      "search",
		Arguments: json.RawMessage(`{"query":"weather"}`),
	}}

	toolResult := validMessage(MessageTypeToolResult)
	toolResult.ToolResult = &ToolResult{
		CallID: "call_1",
		Name:   "search",
		Status: ToolResultSucceeded,
		Output: json.RawMessage(`{"temperature":30}`),
	}

	tests := []struct {
		name    string
		message Message
		wantErr error
	}{
		{name: "system content", message: func() Message { m := content; m.Type = MessageTypeSystem; return m }()},
		{name: "user content", message: content},
		{name: "assistant content", message: func() Message { m := content; m.Type = MessageTypeAssistant; return m }()},
		{name: "tool calls", message: toolCalls},
		{name: "tool result", message: toolResult},
		{name: "empty message ID", message: func() Message { m := content; m.ID = ""; return m }(), wantErr: ErrInvalidMessage},
		{name: "empty session ID", message: func() Message { m := content; m.SessionID = ""; return m }(), wantErr: ErrInvalidMessage},
		{name: "empty turn ID", message: func() Message { m := content; m.TurnID = ""; return m }(), wantErr: ErrInvalidMessage},
		{name: "empty user content", message: func() Message { m := content; m.Content = ""; return m }(), wantErr: ErrInvalidMessage},
		{name: "whitespace system content", message: func() Message { m := content; m.Type = MessageTypeSystem; m.Content = " \t\n "; return m }(), wantErr: ErrInvalidMessage},
		{name: "empty runtime event content", message: func() Message { m := content; m.Type = MessageTypeRuntimeEvent; m.Content = ""; return m }(), wantErr: ErrInvalidMessage},
		{name: "content mixed with tool call", message: func() Message { m := content; m.ToolCalls = toolCalls.ToolCalls; return m }(), wantErr: ErrInvalidMessage},
		{name: "tool call mixed with result", message: func() Message { m := toolCalls; m.ToolResult = toolResult.ToolResult; return m }(), wantErr: ErrInvalidMessage},
		{name: "invalid tool arguments JSON", message: func() Message {
			m := toolCalls
			m.ToolCalls = []ToolCall{{CallID: "call_1", Name: "search", Arguments: json.RawMessage(`{`)}}
			return m
		}(), wantErr: ErrInvalidMessage},
		{name: "invalid tool output JSON", message: func() Message {
			m := toolResult
			m.ToolResult = &ToolResult{CallID: "call_1", Name: "search", Status: ToolResultSucceeded, Output: json.RawMessage(`{`)}
			return m
		}(), wantErr: ErrInvalidMessage},
		{name: "missing tool call ID", message: func() Message {
			m := toolCalls
			m.ToolCalls = []ToolCall{{Name: "search", Arguments: json.RawMessage(`{}`)}}
			return m
		}(), wantErr: ErrInvalidMessage},
		{name: "missing tool result call ID", message: func() Message {
			m := toolResult
			m.ToolResult = &ToolResult{Name: "search", Status: ToolResultSucceeded, Output: json.RawMessage(`{}`)}
			return m
		}(), wantErr: ErrInvalidMessage},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateMessage(test.message)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateMessage() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateMessage() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
		})
	}
}

func TestCloneMessageDoesNotShareMutableValues(t *testing.T) {
	message := testMessage()
	clone := CloneMessage(message)

	message.Content = "changed"
	message.ToolCalls[0].Arguments[2] = 'X'
	message.ToolResult.Output[2] = 'X'

	if clone.Content != "original" {
		t.Fatalf("clone content = %q, want original", clone.Content)
	}
	if string(clone.ToolCalls[0].Arguments) != `{"key":"value"}` {
		t.Fatalf("clone tool arguments = %s", clone.ToolCalls[0].Arguments)
	}
	if string(clone.ToolResult.Output) != `{"ok":true}` {
		t.Fatalf("clone tool output = %s", clone.ToolResult.Output)
	}

	clone.ToolCalls[0].Arguments[2] = 'Y'
	clone.ToolResult.Output[2] = 'Y'
	if string(message.ToolCalls[0].Arguments) != `{"Xey":"value"}` {
		t.Fatalf("input tool arguments changed through clone = %s", message.ToolCalls[0].Arguments)
	}
	if string(message.ToolResult.Output) != `{"Xk":true}` {
		t.Fatalf("input tool output changed through clone = %s", message.ToolResult.Output)
	}
}

func TestCloneMessagesDoesNotShareMutableValues(t *testing.T) {
	messages := []Message{testMessage()}
	clones := CloneMessages(messages)

	clones[0].Content = "changed"
	clones[0].ToolCalls[0].Arguments[2] = 'X'
	clones[0].ToolResult.Output[2] = 'X'

	if messages[0].Content != "original" {
		t.Fatalf("input content changed through clone = %q", messages[0].Content)
	}
	if string(messages[0].ToolCalls[0].Arguments) != `{"key":"value"}` {
		t.Fatalf("input tool arguments changed through clone = %s", messages[0].ToolCalls[0].Arguments)
	}
	if string(messages[0].ToolResult.Output) != `{"ok":true}` {
		t.Fatalf("input tool output changed through clone = %s", messages[0].ToolResult.Output)
	}
}

func testMessage() Message {
	return Message{
		ID:        "msg_1",
		SessionID: "session_1",
		TurnID:    "turn_1",
		Type:      MessageTypeToolCall,
		Content:   "original",
		ToolCalls: []ToolCall{{
			CallID:    "call_1",
			Name:      "search",
			Arguments: json.RawMessage(`{"key":"value"}`),
		}},
		ToolResult: &ToolResult{
			CallID: "call_1",
			Name:   "search",
			Status: ToolResultSucceeded,
			Output: json.RawMessage(`{"ok":true}`),
		},
	}
}
