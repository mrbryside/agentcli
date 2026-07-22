package storage

import "encoding/json"

// CloneMessage returns a deep copy of a stored message.
func CloneMessage(message Message) Message {
	clone := message
	if message.ToolCalls != nil {
		clone.ToolCalls = make([]ToolCall, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			clone.ToolCalls[index] = call
			clone.ToolCalls[index].Arguments = cloneRawMessage(call.Arguments)
		}
	}
	if message.ToolResult != nil {
		result := *message.ToolResult
		result.Output = cloneRawMessage(result.Output)
		clone.ToolResult = &result
	}
	return clone
}

// CloneMessages returns deep copies of stored messages.
func CloneMessages(messages []Message) []Message {
	if messages == nil {
		return nil
	}
	clones := make([]Message, len(messages))
	for index, message := range messages {
		clones[index] = CloneMessage(message)
	}
	return clones
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	clone := make(json.RawMessage, len(raw))
	copy(clone, raw)
	return clone
}
