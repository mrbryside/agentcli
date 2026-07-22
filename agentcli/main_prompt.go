package agentcli

import "strings"

const mainAgentOperatingPrompt = "Handle the user's request directly unless a listed subagent provides material benefit. Use only registered tools and evidence actually returned by them. Tool definitions are supplied separately with each model request. Keep the response focused on the requested outcome and state blockers honestly."

const mainAgentResponsePrompt = "Give the user a clear, self-contained answer. Prefer concise progress reporting while work is active, then finish with the result, important verification, and any unresolved issue."

const modelSecretSafetyPrompt = "Never reveal credentials, authentication tokens, API keys, passwords, private keys, or other secret values in responses, callbacks, summaries, or tool arguments. If secret material is encountered despite tool protections, omit or replace the value with [REDACTED], warn that it may be exposed, and continue using only non-secret metadata."

func (project *Project) mainAgentSystemPrompt() string {
	if project == nil {
		return ""
	}
	sections := []string{
		"You are the primary agent for this session. Help the user accomplish their requested outcome while respecting the capabilities and instructions provided to you.",
		"# Runtime context\n\n" + renderPromptRuntimeContext(project, promptRuntimeContext{
			Agent: "main", Provider: project.providerName, Model: project.modelName,
		}),
		"# Operating principles\n\n" + mainAgentOperatingPrompt,
		"# Sensitive information\n\n" + modelSecretSafetyPrompt,
	}
	if instructions := strings.TrimSpace(project.main.Instructions); instructions != "" {
		sections = append(sections, "# Main agent instructions\n\n"+instructions)
	}
	if len(project.skills) != 0 {
		sections = append(sections, "# Skills\n\n"+project.skillDiscoveryPrompt())
	}
	if len(project.subagents) != 0 {
		sections = append(sections, "# Subagents\n\n"+project.subagentDiscoveryPrompt())
	}
	sections = append(sections, "# Response contract\n\n"+mainAgentResponsePrompt)
	return strings.Join(sections, "\n\n")
}
