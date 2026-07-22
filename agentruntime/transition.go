package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
)

// Effects derives the coordinator commands for event using only the previous
// immutable state. It never performs I/O or mutates either input.
func Effects(current AgentState, event AgentEvent) ([]Effect, error) {
	if isTerminal(current) {
		return nil, nil
	}

	var effects []Effect
	switch event.Type {
	case RunStarted:
		if event.Message == nil {
			effects = failEffects(event, errors.New("run started without a message"))
			break
		}
		effects = []Effect{
			{Type: AppendMessages, Messages: []Message{storage.CloneMessage(*event.Message)}},
			{Type: StartProvider},
		}
	case ProviderEventReceived:
		effects = providerEffects(event)
	case ToolCallRequested:
		if event.ToolRequest == nil {
			effects = failEffects(event, errors.New("tool call requested without a request"))
			break
		}
		request := cloneToolRequest(*event.ToolRequest)
		effects = []Effect{{Type: DispatchTool, ToolRequest: &request}}
	case ToolResultReceived:
		effects = toolResultEffects(current, event)
	case RunCompleted:
		state := State(current, event)
		result, err := Result(Events(state))
		if err != nil {
			effects = failEffects(event, fmt.Errorf("derive completed run result: %w", err))
			break
		}
		effects = []Effect{{Type: CompleteRun, Result: &result}, {Type: CloseRun}}
	case RunFailed:
		err := event.Error
		if err == nil {
			err = errors.New("agent run failed")
		}
		effects = []Effect{{Type: FailRun, Error: err}, {Type: CloseRun}}
	case AgentInterrupted:
		effects = interruptionEffects(current, event)
	}

	return cloneEffects(effects), nil
}

func providerEffects(event AgentEvent) []Effect {
	if event.ProviderEvent.Type == "" {
		return failEffects(event, errors.New("provider event received without a provider event"))
	}
	providerEvent := event.ProviderEvent
	switch providerEvent.Type {
	case provider.StreamFailed:
		return failEffects(event, providerEventError(providerEvent))
	case provider.StreamCompleted:
		result, _ := terminalProviderResult(providerEvent)
		if result.Content == "" {
			result.Content = providerEvent.Content
		}
		if result.Reasoning == "" {
			result.Reasoning = providerEvent.Reasoning
		}
		if len(result.CompletedTools) == 0 {
			message := Message{
				SessionID: event.SessionID,
				TurnID:    event.TurnID,
				Type:      MessageTypeAssistant,
				Content:   result.Content,
				Reasoning: result.Reasoning,
			}
			completed := AgentEvent{SessionID: event.SessionID, TurnID: event.TurnID, Type: RunCompleted}
			return []Effect{
				{Type: AppendMessages, Messages: []Message{message}},
				{Type: EmitEvent, Event: &completed},
			}
		}

		calls := make([]ToolCall, len(result.CompletedTools))
		for index, providerCall := range result.CompletedTools {
			if providerCall.ID == "" {
				return failEffects(event, fmt.Errorf("provider tool %d has an empty call ID", index))
			}
			if providerCall.Name == "" {
				return failEffects(event, fmt.Errorf("provider tool %q has an empty name", providerCall.ID))
			}
			arguments, err := json.Marshal(providerCall.Arguments)
			if err != nil {
				return failEffects(event, fmt.Errorf("encode tool %q arguments: %w", providerCall.Name, err))
			}
			calls[index] = ToolCall{CallID: providerCall.ID, Name: providerCall.Name, Arguments: arguments}
		}
		message := Message{
			SessionID: event.SessionID,
			TurnID:    event.TurnID,
			Type:      MessageTypeToolCall,
			Content:   result.Content,
			Reasoning: result.Reasoning,
			ToolCalls: calls,
		}
		effects := []Effect{{Type: AppendMessages, Messages: []Message{message}}}
		for _, call := range calls {
			request := ToolRequest{SessionID: event.SessionID, TurnID: event.TurnID, Call: call}
			requested := AgentEvent{SessionID: event.SessionID, TurnID: event.TurnID, Type: ToolCallRequested, ToolRequest: &request}
			effects = append(effects, Effect{Type: EmitEvent, Event: &requested})
		}
		return effects
	default:
		return nil
	}
}

func toolResultEffects(current AgentState, event AgentEvent) []Effect {
	if event.ToolResult == nil {
		return failEffects(event, errors.New("tool result received without a result"))
	}
	round, ok := latestToolRound(Events(current))
	if !ok {
		return failEffects(event, errors.New("tool result received without a pending tool round"))
	}
	result := event.ToolResult
	if result.SessionID != event.SessionID || result.TurnID != event.TurnID {
		return failEffects(event, fmt.Errorf("tool result identifiers do not match the run"))
	}
	if _, exists := round.requested[result.Result.CallID]; !exists {
		return failEffects(event, fmt.Errorf("tool result has unknown call ID %q", result.Result.CallID))
	}
	if _, duplicate := round.accepted[result.Result.CallID]; duplicate {
		return nil
	}
	round.accepted[result.Result.CallID] = cloneToolResult(result.Result)
	if len(round.accepted) != len(round.order) {
		return nil
	}

	messages := make([]Message, 0, len(round.order))
	for _, callID := range round.order {
		toolResult := round.accepted[callID]
		messages = append(messages, Message{
			SessionID:  event.SessionID,
			TurnID:     event.TurnID,
			Type:       MessageTypeToolResult,
			ToolResult: &toolResult,
		})
	}
	return []Effect{{Type: AppendMessages, Messages: messages}, {Type: StartProvider}}
}

func interruptionEffects(current AgentState, event AgentEvent) []Effect {
	round, ok := latestToolRound(Events(current))
	if !ok || len(round.order) == 0 {
		return []Effect{{Type: CancelProvider}, {Type: CloseRun}}
	}
	reason := event.Reason
	if reason == "" {
		reason = ErrRunInterrupted.Error()
	}
	pendingIDs := make([]string, 0, len(round.order))
	results := make([]Message, 0, len(round.order))
	for _, callID := range round.order {
		if accepted, ok := round.accepted[callID]; ok {
			accepted := cloneToolResult(accepted)
			results = append(results, Message{SessionID: event.SessionID, TurnID: event.TurnID, Type: MessageTypeToolResult, ToolResult: &accepted})
			continue
		}
		request := round.requested[callID]
		pendingIDs = append(pendingIDs, callID)
		result := ToolResult{CallID: callID, Name: request.Call.Name, Status: ToolResultInterrupted, Error: reason}
		results = append(results, Message{SessionID: event.SessionID, TurnID: event.TurnID, Type: MessageTypeToolResult, ToolResult: &result})
	}

	effects := make([]Effect, 0, 4)
	if len(pendingIDs) > 0 {
		interrupt := ToolInterrupt{SessionID: event.SessionID, TurnID: event.TurnID, CallIDs: pendingIDs, Reason: reason}
		effects = append(effects, Effect{Type: InterruptTools, ToolInterrupt: &interrupt})
	}
	effects = append(effects, Effect{Type: CancelProvider})
	if len(results) > 0 {
		effects = append(effects, Effect{Type: AppendMessages, Messages: results})
	}
	effects = append(effects, Effect{Type: CloseRun})
	return effects
}

type toolRound struct {
	order     []string
	requested map[string]ToolRequest
	accepted  map[string]ToolResult
}

func latestToolRound(events []AgentEvent) (toolRound, bool) {
	boundary := -1
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != ProviderEventReceived || event.ProviderEvent.Type != provider.StreamCompleted {
			continue
		}
		result, _ := terminalProviderResult(event.ProviderEvent)
		if len(result.CompletedTools) > 0 {
			boundary = index
			break
		}
	}
	if boundary == -1 {
		return toolRound{}, false
	}

	round := toolRound{requested: make(map[string]ToolRequest), accepted: make(map[string]ToolResult)}
	for _, event := range events[boundary+1:] {
		switch event.Type {
		case ToolCallRequested:
			if event.ToolRequest == nil || event.ToolRequest.Call.CallID == "" {
				continue
			}
			callID := event.ToolRequest.Call.CallID
			if _, duplicate := round.requested[callID]; duplicate {
				continue
			}
			round.order = append(round.order, callID)
			round.requested[callID] = cloneToolRequest(*event.ToolRequest)
		case ToolResultReceived:
			if event.ToolResult == nil {
				continue
			}
			callID := event.ToolResult.Result.CallID
			if _, requested := round.requested[callID]; !requested {
				continue
			}
			if _, duplicate := round.accepted[callID]; !duplicate {
				round.accepted[callID] = cloneToolResult(event.ToolResult.Result)
			}
		}
	}
	return round, len(round.order) > 0
}

func failEffects(event AgentEvent, err error) []Effect {
	failed := AgentEvent{SessionID: event.SessionID, TurnID: event.TurnID, Type: RunFailed, Error: err}
	return []Effect{{Type: EmitEvent, Event: &failed}}
}

func providerEventError(event provider.StreamEvent) error {
	if event.Error != nil {
		return event.Error
	}
	switch payload := event.Payload.(type) {
	case provider.StreamFailedPayload:
		if payload.Error != nil {
			return payload.Error
		}
	case *provider.StreamFailedPayload:
		if payload != nil && payload.Error != nil {
			return payload.Error
		}
	}
	return errors.New("provider stream failed")
}

func isTerminal(state AgentState) bool {
	for _, event := range Events(state) {
		switch event.Type {
		case RunCompleted, RunFailed, AgentInterrupted:
			return true
		}
	}
	return false
}
