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

	checkpoint := validMessage(MessageTypeCompactionCheckpoint)
	checkpoint.CompactionCheckpoint = &CompactionCheckpoint{
		Summary:                "Earlier work completed successfully.",
		CoversThroughMessageID: "message-10",
		TailStartMessageID:     "message-11",
	}

	tests := []struct {
		name    string
		message Message
		wantErr error
	}{
		{name: "system content", message: func() Message { m := content; m.Type = MessageTypeSystem; return m }()},
		{name: "user content", message: content},
		{name: "assistant content", message: func() Message { m := content; m.Type = MessageTypeAssistant; return m }()},
		{name: "assistant reasoning", message: func() Message { m := content; m.Type = MessageTypeAssistant; m.Reasoning = "considering"; return m }()},
		{name: "tool calls", message: toolCalls},
		{name: "tool calls with reasoning", message: func() Message { m := toolCalls; m.Reasoning = "selecting a tool"; return m }()},
		{name: "tool result", message: toolResult},
		{name: "compaction checkpoint", message: checkpoint},
		{name: "compaction checkpoint accepts unknown boundaries for runtime validation", message: func() Message {
			m := checkpoint
			m.CompactionCheckpoint = &CompactionCheckpoint{Summary: "summary", CoversThroughMessageID: "unknown-earlier", TailStartMessageID: "unknown-later"}
			return m
		}()},
		{name: "missing compaction checkpoint", message: validMessage(MessageTypeCompactionCheckpoint), wantErr: ErrInvalidMessage},
		{name: "empty checkpoint summary", message: func() Message {
			m := checkpoint
			m.CompactionCheckpoint = &CompactionCheckpoint{CoversThroughMessageID: "message-10", TailStartMessageID: "message-11"}
			return m
		}(), wantErr: ErrInvalidMessage},
		{name: "whitespace checkpoint summary", message: func() Message {
			m := checkpoint
			m.CompactionCheckpoint = &CompactionCheckpoint{Summary: " \t", CoversThroughMessageID: "message-10", TailStartMessageID: "message-11"}
			return m
		}(), wantErr: ErrInvalidMessage},
		{name: "missing covers-through boundary", message: func() Message {
			m := checkpoint
			m.CompactionCheckpoint = &CompactionCheckpoint{Summary: "summary", TailStartMessageID: "message-11"}
			return m
		}(), wantErr: ErrInvalidMessage},
		{name: "whitespace covers-through boundary", message: func() Message {
			m := checkpoint
			m.CompactionCheckpoint = &CompactionCheckpoint{Summary: "summary", CoversThroughMessageID: " \t", TailStartMessageID: "message-11"}
			return m
		}(), wantErr: ErrInvalidMessage},
		{name: "missing tail-start boundary", message: func() Message {
			m := checkpoint
			m.CompactionCheckpoint = &CompactionCheckpoint{Summary: "summary", CoversThroughMessageID: "message-10"}
			return m
		}(), wantErr: ErrInvalidMessage},
		{name: "whitespace tail-start boundary", message: func() Message {
			m := checkpoint
			m.CompactionCheckpoint = &CompactionCheckpoint{Summary: "summary", CoversThroughMessageID: "message-10", TailStartMessageID: " \t"}
			return m
		}(), wantErr: ErrInvalidMessage},
		{name: "checkpoint with content", message: func() Message { m := checkpoint; m.Content = "not allowed"; return m }(), wantErr: ErrInvalidMessage},
		{name: "checkpoint with reasoning", message: func() Message { m := checkpoint; m.Reasoning = "not allowed"; return m }(), wantErr: ErrInvalidMessage},
		{name: "checkpoint with tool calls", message: func() Message { m := checkpoint; m.ToolCalls = toolCalls.ToolCalls; return m }(), wantErr: ErrInvalidMessage},
		{name: "checkpoint with tool result", message: func() Message { m := checkpoint; m.ToolResult = toolResult.ToolResult; return m }(), wantErr: ErrInvalidMessage},
		{name: "user with checkpoint", message: func() Message { m := content; m.CompactionCheckpoint = checkpoint.CompactionCheckpoint; return m }(), wantErr: ErrInvalidMessage},
		{name: "user reasoning", message: func() Message { m := content; m.Reasoning = "private"; return m }(), wantErr: ErrInvalidMessage},
		{name: "tool result reasoning", message: func() Message { m := toolResult; m.Reasoning = "private"; return m }(), wantErr: ErrInvalidMessage},
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

func TestCloneMessageDoesNotShareCompactionCheckpoint(t *testing.T) {
	message := Message{
		ID: "checkpoint-1", SessionID: "session-1", TurnID: "turn-1", Type: MessageTypeCompactionCheckpoint,
		CompactionCheckpoint: &CompactionCheckpoint{Summary: "original", CoversThroughMessageID: "message-10", TailStartMessageID: "message-11"},
	}
	clone := CloneMessage(message)

	clone.CompactionCheckpoint.Summary = "changed"
	clone.CompactionCheckpoint.CoversThroughMessageID = "changed-message"
	if message.CompactionCheckpoint.Summary != "original" {
		t.Fatalf("input summary changed through clone = %q", message.CompactionCheckpoint.Summary)
	}
	if message.CompactionCheckpoint.CoversThroughMessageID != "message-10" {
		t.Fatalf("input boundary changed through clone = %q", message.CompactionCheckpoint.CoversThroughMessageID)
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
