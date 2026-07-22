package agentruntime

import "github.com/mrbryside/agentcli/provider"

// RunResult is the aggregate outcome of a completed agent turn.
type RunResult struct {
	SessionID   string
	TurnID      string
	Content     string
	Reasoning   string
	ToolResults []ToolResult
	Steps       int
	Finished    bool
}

// Result folds an ordered AgentEvent history into a terminal RunResult.
func Result(events []AgentEvent) (RunResult, error) {
	result := RunResult{}
	requestedCallIDs := make([]string, 0)
	acceptedResults := make(map[string]ToolResult)
	var roundContent, roundReasoning string

	for _, event := range events {
		if result.SessionID == "" && event.SessionID != "" {
			result.SessionID = event.SessionID
		}
		if result.TurnID == "" && event.TurnID != "" {
			result.TurnID = event.TurnID
		}

		switch event.Type {
		case ProviderEventReceived:
			roundContent += event.ProviderEvent.Content
			roundReasoning += event.ProviderEvent.Reasoning
			if providerResult, ok := terminalProviderResult(event.ProviderEvent); ok {
				if providerResult.Content == "" {
					providerResult.Content = roundContent
				}
				if providerResult.Reasoning == "" {
					providerResult.Reasoning = roundReasoning
				}
				result.Content = providerResult.Content
				result.Reasoning = providerResult.Reasoning
				result.Steps++
				roundContent, roundReasoning = "", ""
			}
		case ToolCallRequested:
			if event.ToolRequest != nil && event.ToolRequest.Call.CallID != "" {
				requestedCallIDs = append(requestedCallIDs, event.ToolRequest.Call.CallID)
			}
		case ToolResultReceived:
			if event.ToolResult != nil && event.ToolResult.Result.CallID != "" {
				callID := event.ToolResult.Result.CallID
				if _, alreadyAccepted := acceptedResults[callID]; !alreadyAccepted {
					acceptedResults[callID] = cloneToolResult(event.ToolResult.Result)
				}
			}
		case RunCompleted:
			for _, callID := range requestedCallIDs {
				if toolResult, ok := acceptedResults[callID]; ok {
					result.ToolResults = append(result.ToolResults, cloneToolResult(toolResult))
				}
			}
			result.Finished = true
			return cloneRunResult(result), nil
		case RunFailed:
			return RunResult{}, event.Error
		case AgentInterrupted:
			return RunResult{}, ErrRunInterrupted
		}
	}

	return RunResult{}, ErrRunNotDone
}

func terminalProviderResult(event provider.StreamEvent) (provider.StreamResult, bool) {
	if event.Type != provider.StreamCompleted {
		return provider.StreamResult{}, false
	}
	switch payload := event.Payload.(type) {
	case provider.StreamCompletedPayload:
		return cloneProviderResult(payload.Result), true
	case *provider.StreamCompletedPayload:
		if payload != nil {
			return cloneProviderResult(payload.Result), true
		}
	}
	return provider.StreamResult{}, true
}

func cloneToolResult(result ToolResult) ToolResult {
	clone := result
	clone.Output = cloneRawJSON(result.Output)
	return clone
}

func cloneRunResult(result RunResult) RunResult {
	clone := result
	if result.ToolResults != nil {
		clone.ToolResults = make([]ToolResult, len(result.ToolResults))
		for index, toolResult := range result.ToolResults {
			clone.ToolResults[index] = cloneToolResult(toolResult)
		}
	}
	return clone
}
