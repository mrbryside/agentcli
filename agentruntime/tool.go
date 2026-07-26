package agentruntime

import "encoding/json"

// ToolTurnBehavior controls what the agent loop does after a tool batch has
// been persisted. The zero value preserves the normal tool loop.
type ToolTurnBehavior string

const (
	ToolTurnContinue     ToolTurnBehavior = ""
	ToolTurnEnd          ToolTurnBehavior = "end_turn"
	ToolTurnEndOnSuccess ToolTurnBehavior = "end_turn_on_success"
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
	// ProviderStep is the one-based provider round that emitted the call.
	// Runtimes set it before dispatch so execution policies can distinguish an
	// initial tool-first response from work performed on a later round.
	ProviderStep       int
	CompletionBoundary bool
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
	clone.Result = cloneToolResult(envelope.Result)
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
