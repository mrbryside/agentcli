package agentruntime

import "github.com/mrbryside/agentcli/storage"

// EffectType identifies a command that the Run coordinator interprets.
type EffectType string

const (
	EmitEvent      EffectType = "emit_event"
	AppendMessages EffectType = "append_messages"
	StartProvider  EffectType = "start_provider"
	DispatchTool   EffectType = "dispatch_tool"
	InterruptTools EffectType = "interrupt_tools"
	CancelProvider EffectType = "cancel_provider"
	CompleteRun    EffectType = "complete_run"
	FailRun        EffectType = "fail_run"
	CloseRun       EffectType = "close_run"
)

// Effect is a pure command for the Run coordinator. Only the field relevant
// to Type is populated. All mutable payloads are copied before being returned
// from Effects.
type Effect struct {
	Type          EffectType
	Event         *AgentEvent
	Messages      []Message
	ToolRequest   *ToolRequest
	ToolInterrupt *ToolInterrupt
	Result        *RunResult
	Error         error
}

func cloneEffect(effect Effect) Effect {
	clone := effect
	if effect.Event != nil {
		event := cloneEvent(*effect.Event)
		clone.Event = &event
	}
	clone.Messages = storage.CloneMessages(effect.Messages)
	if effect.ToolRequest != nil {
		request := cloneToolRequest(*effect.ToolRequest)
		clone.ToolRequest = &request
	}
	if effect.ToolInterrupt != nil {
		interrupt := cloneToolInterrupt(*effect.ToolInterrupt)
		clone.ToolInterrupt = &interrupt
	}
	if effect.Result != nil {
		result := cloneRunResult(*effect.Result)
		clone.Result = &result
	}
	return clone
}

func cloneEffects(effects []Effect) []Effect {
	if effects == nil {
		return nil
	}
	clones := make([]Effect, len(effects))
	for index, effect := range effects {
		clones[index] = cloneEffect(effect)
	}
	return clones
}
