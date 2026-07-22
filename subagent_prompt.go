package agentcli

import (
	"errors"
	"fmt"
	"strings"
)

const subagentCapabilityBoundaryPrompt = "Use only information present in the conversation or obtained through the capabilities registered for this child. Never claim to have accessed a resource, performed an action, or gathered evidence unless the conversation or a tool result supports it. If the delegated task requires an unavailable capability, say so clearly and stop; do not substitute an unrelated skill. You cannot create, inspect, message, wait for, or close subagents, and you cannot delegate to another agent; only the parent owns subagent orchestration."

const subagentCompletionPrompt = "Before your final assistant answer, you MUST call report_subagent_outcome exactly once. Report completed only when you are confident the delegated task is fully resolved and no required work remains. Report incomplete when blocked, waiting for information, partially done, or when the parent/user must provide or decide something; include the required next_step. If unsure, report incomplete. If you attempt to finish without a successful report, the runtime grants one repair round exposing only report_subagent_outcome; use it only to report the existing outcome and never repeat domain work. Omitting the report again defaults the callback to incomplete. After the tool succeeds, finish the turn with a concise, self-contained final answer for the parent agent. State the result or conclusion, the evidence that materially supports it, and any unresolved blocker or next step. Never finish with only a progress update, tool status, or promise to report back. Tool calls and intermediate work remain in the child transcript, so do not reproduce their full trace in the final answer."

func withChildSystemPrompts(project *Project, definition SubagentDefinition) Option {
	return func(configuration *config) error {
		if project == nil {
			return errors.New("project is required")
		}
		configuration.systemPrompts = append(configuration.systemPrompts, subagentSystemPrompt(project, definition))
		if strings.TrimSpace(project.agents) != "" {
			configuration.systemPrompts = append(configuration.systemPrompts, project.agents)
		}
		return nil
	}
}

func subagentSystemPrompt(project *Project, definition SubagentDefinition) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "You are the configured %q subagent. Complete delegated work independently and return a useful result to the parent agent.", definition.Name)

	prompt.WriteString("\n\n# Assignment role\n\n")
	prompt.WriteString(strings.TrimSpace(definition.Instructions))

	prompt.WriteString("\n\n# Runtime context\n\n")
	prompt.WriteString(renderPromptRuntimeContext(project, promptRuntimeContext{
		Agent: definition.Name, Provider: definition.Provider, Model: definition.Model,
	}))

	prompt.WriteString("\n\n# Evidence and tool use\n\n")
	prompt.WriteString(subagentCapabilityBoundaryPrompt)
	prompt.WriteString(" Tool definitions are supplied separately with each model request.")

	prompt.WriteString("\n\n# Sensitive information\n\n")
	prompt.WriteString(modelSecretSafetyPrompt)

	if project != nil && len(project.skills) != 0 {
		prompt.WriteString("\n\n# Skills\n\n")
		prompt.WriteString(project.skillDiscoveryPrompt())
	}

	prompt.WriteString("\n\n# Delivery contract\n\n")
	prompt.WriteString(subagentCompletionPrompt)
	return prompt.String()
}
