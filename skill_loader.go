package agentcli

import "github.com/mrbryside/agentcli/toolexecution"

// SkillReloadPolicy remains an agentcli alias for compatibility; execution
// and policy ownership live in toolexecution.
type SkillReloadPolicy = toolexecution.SkillReloadPolicy

func DefaultSkillReloadPolicy() SkillReloadPolicy {
	return toolexecution.DefaultSkillReloadPolicy()
}

func (project *Project) executionSkills() []toolexecution.Skill {
	skills := make([]toolexecution.Skill, 0, len(project.skills))
	for _, skill := range project.skills {
		skills = append(skills, toolexecution.Skill{
			Name: skill.Name, Description: skill.Description, Instructions: skill.Instructions,
		})
	}
	return skills
}
