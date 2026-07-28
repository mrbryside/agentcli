package agentcli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const subagentCapabilityBoundaryPrompt = "Use only information present in the conversation or obtained through capabilities registered for this subagent. Never claim to have accessed a resource, performed an action, or gathered evidence unless a message or tool result supports it. The tools listed with the current request are available now and are the authoritative tool list. If a tool name is listed, do not claim it is missing or ask the user to enable it. If the assigned task requires an unavailable capability, say so clearly and stop; do not substitute an unrelated skill. You cannot use task or manage other agent sessions, and you cannot delegate to another agent."

const subagentCompletionPrompt = "After completing the assigned work, respond once with a concise, self-contained final answer. Include the result, material evidence, and any important limitation or next step. Do not reproduce the full tool trace. If essential information is missing, do not guess or take the action. State that no action happened and return one exact question for the user. The same task can receive the user's answer later."

func withSubagentSystemPrompts(project *Project, definition SubagentDefinition) Option {
	return func(configuration *config) error {
		if project == nil {
			return errors.New("project is required")
		}
		configuration.systemPrompts = append(configuration.systemPrompts, subagentSystemPrompt(project, definition))
		if instructions := strings.TrimSpace(definition.Instructions); instructions != "" {
			configuration.systemPrompts = append(configuration.systemPrompts, "# Assignment role\n\n"+instructions)
		}
		return nil
	}
}

func subagentSystemPrompt(project *Project, definition SubagentDefinition) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "You are the configured %q subagent. Complete the assigned work independently and return a useful result to the main agent.", definition.Name)

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
	if resultContractPrompt := subagentResultContractPrompt(definition); resultContractPrompt != "" {
		prompt.WriteString("\n\n# Final response format\n\n")
		prompt.WriteString(resultContractPrompt)
	}
	return prompt.String()
}

func subagentResultContractPrompt(definition SubagentDefinition) string {
	contract := definition.Result
	if contract == nil {
		return ""
	}
	var prompt strings.Builder
	prompt.WriteString("Return exactly one JSON object and no Markdown or surrounding text. Use only these fields:\n\n")
	fmt.Fprintf(&prompt, "- %q: required string containing the user-facing final answer.\n", contract.MessageField)
	metadataNames := make([]string, 0, len(contract.Metadata))
	for name := range contract.Metadata {
		metadataNames = append(metadataNames, name)
	}
	sort.Strings(metadataNames)
	for _, name := range metadataNames {
		field := contract.Metadata[name]
		requirement := "optional"
		if field.Required {
			requirement = "required"
		}
		fmt.Fprintf(&prompt, "- %q: %s %s metadata field.\n", name, requirement, field.Type)
	}
	return prompt.String()
}
