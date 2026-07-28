package agentcli

import (
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestValidateToolRequiredSkillsUsesSkillsAvailableToCurrentAgent(t *testing.T) {
	tool := toolexecution.Tool{
		Definition:     agentruntime.ToolDefinition{Name: "web_search"},
		RequiredSkills: []string{"web-research"},
	}
	if err := validateToolRequiredSkills(
		&Project{skills: map[string]Skill{"web-research": {Name: "web-research"}}},
		[]toolexecution.Tool{tool},
	); err != nil {
		t.Fatalf("validateToolRequiredSkills() error = %v", err)
	}
	if err := validateToolRequiredSkills(nil, []toolexecution.Tool{tool}); err == nil ||
		!strings.Contains(err.Error(), "no project skills are configured") {
		t.Fatalf("nil project error = %v", err)
	}
	if err := validateToolRequiredSkills(
		&Project{skills: map[string]Skill{}},
		[]toolexecution.Tool{tool},
	); err == nil || !strings.Contains(err.Error(), "not available to this agent") {
		t.Fatalf("missing skill error = %v", err)
	}
}

func TestValidateProjectToolAllowlistsChecksSubagentRequiredSkillsAtStartup(t *testing.T) {
	tool := toolexecution.Tool{
		Definition:     agentruntime.ToolDefinition{Name: "fetch_web"},
		RequiredSkills: []string{"web-reading"},
	}
	project := &Project{subagents: map[string]SubagentDefinition{
		"reader": {
			Name:   "reader",
			Tools:  []string{"fetch_web"},
			Skills: nil,
		},
	}}
	if err := validateProjectToolAllowlists(project, []toolexecution.Tool{tool}); err == nil ||
		!strings.Contains(err.Error(), `subagent "reader"`) ||
		!strings.Contains(err.Error(), `requires skill "web-reading"`) {
		t.Fatalf("missing subagent skill error = %v", err)
	}

	definition := project.subagents["reader"]
	definition.Skills = []string{"web-reading"}
	project.subagents["reader"] = definition
	if err := validateProjectToolAllowlists(project, []toolexecution.Tool{tool}); err != nil {
		t.Fatalf("validateProjectToolAllowlists() error = %v", err)
	}
}
