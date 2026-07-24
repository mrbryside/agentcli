package agentruntime

import "encoding/json"

// ToolTurnBehavior controls what the agent loop does after a successful tool
// result has been persisted. The zero value preserves the normal tool loop.
type ToolTurnBehavior string

const (
	ToolTurnContinue ToolTurnBehavior = ""
	ToolTurnEnd      ToolTurnBehavior = "end_turn"
)

// ToolDefinition describes a provider-neutral callable tool.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema ToolSchema
}

// ToolRequest is sent by a runtime to the shared tool worker channel.
type ToolRequest struct {
	SessionID string
	TurnID    string
	Call      ToolCall
}

// ToolResultEnvelope correlates a completed tool result to its turn.
type ToolResultEnvelope struct {
	SessionID    string
	TurnID       string
	Result       ToolResult
	TurnBehavior ToolTurnBehavior
}

// ToolInterrupt requests cancellation of selected outstanding calls in a turn.
type ToolInterrupt struct {
	SessionID string
	TurnID    string
	CallIDs   []string
	Reason    string
}

func cloneToolDefinition(definition ToolDefinition) ToolDefinition {
	clone := definition
	clone.InputSchema = definition.InputSchema.Clone()
	return clone
}

func cloneToolDefinitions(definitions []ToolDefinition) []ToolDefinition {
	if definitions == nil {
		return nil
	}
	clones := make([]ToolDefinition, len(definitions))
	for index, definition := range definitions {
		clones[index] = cloneToolDefinition(definition)
	}
	return clones
}

func cloneToolRequest(request ToolRequest) ToolRequest {
	clone := request
	clone.Call.Arguments = cloneRawJSON(request.Call.Arguments)
	return clone
}

func cloneToolResultEnvelope(envelope ToolResultEnvelope) ToolResultEnvelope {
	clone := envelope
	clone.Result.Output = cloneRawJSON(envelope.Result.Output)
	return clone
}

func cloneToolInterrupt(interrupt ToolInterrupt) ToolInterrupt {
	clone := interrupt
	if interrupt.CallIDs != nil {
		clone.CallIDs = make([]string, len(interrupt.CallIDs))
		copy(clone.CallIDs, interrupt.CallIDs)
	}
	return clone
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	clone := make(json.RawMessage, len(raw))
	copy(clone, raw)
	return clone
}
