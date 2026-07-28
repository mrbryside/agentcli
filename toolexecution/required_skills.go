package toolexecution

import (
	"context"
	"encoding/json"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
)

func missingRequiredSkills(
	ctx context.Context,
	messages storage.MessageStorage,
	sessionID string,
	turnID string,
	required []string,
) ([]string, error) {
	history, err := messages.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	loaded := make(map[string]struct{}, len(required))
	for _, message := range history {
		if message.TurnID != turnID ||
			message.Type != storage.MessageTypeToolResult ||
			message.ToolResult == nil ||
			message.ToolResult.Name != SkillLoaderToolName ||
			message.ToolResult.Status != storage.ToolResultSucceeded {
			continue
		}
		var result SkillToolResult
		if json.Unmarshal(message.ToolResult.Output, &result) != nil ||
			result.Status != "loaded" ||
			result.Name == "" {
			continue
		}
		loaded[result.Name] = struct{}{}
	}

	missing := make([]string, 0, len(required))
	for _, name := range required {
		if _, found := loaded[name]; !found {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

func requiredSkillsResult(
	request agentruntime.ToolRequest,
	required []string,
	missing []string,
) agentruntime.ToolResultEnvelope {
	output, _ := json.Marshal(map[string]any{
		"status":          "blocked",
		"executed":        false,
		"reason":          "required_skill_not_loaded",
		"required_skills": required,
		"missing_skills":  missing,
		"instruction": "Call load_skill once for each missing skill. A status=loaded result for " +
			"that skill in this turn satisfies the requirement, including when " +
			"instructions_in_context=true. Then retry this tool.",
	})
	return agentruntime.ToolResultEnvelope{
		SessionID: request.SessionID,
		TurnID:    request.TurnID,
		Result: agentruntime.ToolResult{
			CallID:           request.Call.CallID,
			Name:             request.Call.Name,
			Status:           agentruntime.ToolResultSucceeded,
			Output:           output,
			TriggerSatisfied: boolPointer(false),
		},
	}
}
