package agentcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerGeneratesMainAgentWithStarterSkill(t *testing.T) {
	installer := readInstaller(t)
	mainDefinition := installerHeredoc(t, installer, `cat >"$target/.agentcli/MAIN.md" <<'EOF'`)

	const expected = `
---
provider: replace-provider
model: replace-model
skills:
  - interview
tools:
  - glob
  - read
---

You are chatbot.`
	if strings.TrimSpace(mainDefinition) != strings.TrimSpace(expected) {
		t.Fatalf("installer MAIN.md =\n%s\nwant\n%s", mainDefinition, expected)
	}
}

func TestInstallerIncludesOnlyReadAndGlobTools(t *testing.T) {
	installer := readInstaller(t)
	for _, required := range []string{
		"AGENTCLI_TOOL_READ_URL",
		"AGENTCLI_TOOL_GLOB_URL",
		"agentcli.WithTool(newGlobTool(projectRoot))",
		"agentcli.WithTool(newReadTool(projectRoot))",
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer does not contain %q", required)
		}
	}
	for _, forbidden := range []string{
		"AGENTCLI_TOOL_EDIT_URL",
		"AGENTCLI_TOOL_REPORT_DISCORD_URL",
		"newEditTool",
		"newReportDiscordTool",
		"guardrails",
		"GUARDRAILS_API_KEY",
	} {
		if strings.Contains(installer, forbidden) {
			t.Fatalf("installer still contains %q", forbidden)
		}
	}
}

func TestInstallerConfigOmitsLoggingAndObservability(t *testing.T) {
	config := installerHeredoc(t, readInstaller(t), `cat >"$target/.agentcli/config.yaml" <<'EOF'`)
	for _, forbidden := range []string{"logging:", "observability:", "langfuse:"} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("installer config still contains %q:\n%s", forbidden, config)
		}
	}
	for _, required := range []string{
		"permission_mode: criticalOnly",
		"compaction:\n  auto: true\n  provider: replace-provider\n  model: replace-model",
		"providers:\n  replace-provider:",
		"# Controls which declared tool risks require approval.",
		"# Automatically summarizes older transcript content",
		"# Provider names are local aliases.",
		"# Model entries are exact-name overrides, not an allowlist.",
	} {
		if !strings.Contains(config, required) {
			t.Fatalf("installer config does not contain %q:\n%s", required, config)
		}
	}
}

func TestInstallerFallbackVersionTracksCurrentRelease(t *testing.T) {
	if !strings.Contains(readInstaller(t), "agentcli_fallback_version=v0.0.62") {
		t.Fatal("installer fallback version does not track v0.0.62")
	}
}

func TestInstallerIncludesDocumentedModelOverride(t *testing.T) {
	const required = `# Model entries are exact-name overrides, not an allowlist. These limits
    # help AgentCLI budget context and compaction when discovery is unavailable.
    models:
      replace-model:
        context_window_tokens: 122880 # Total input and output capacity.
        max_output_tokens: 66560 # Maximum output supported by the endpoint.
        # extra_body is merged into each request for this exact model. This
        # disables DeepSeek-style thinking; remove it for incompatible APIs.
        extra_body:
          thinking:
            type: disabled`
	if installer := readInstaller(t); !strings.Contains(installer, required) {
		t.Fatalf("installer model override does not contain:\n%s", required)
	}
}

func TestInstallerIncludesStarterSkillAndTaskAgent(t *testing.T) {
	installer := readInstaller(t)
	skill := installerHeredoc(t, installer, `cat >"$target/.agentcli/skill/interview/SKILL.md" <<'EOF'`)
	for _, required := range []string{
		"name: interview",
		"description: Use when the request is unclear",
		"# Requirements interview",
	} {
		if !strings.Contains(skill, required) {
			t.Fatalf("installer skill does not contain %q:\n%s", required, skill)
		}
	}

	taskAgent := installerHeredoc(t, installer, `cat >"$target/.agentcli/agent/researcher/researcher.md" <<'EOF'`)
	for _, required := range []string{
		"name: researcher",
		"description: Use for substantial technical research",
		"provider: replace-provider",
		"model: replace-model",
		"tools:\n  - glob\n  - read",
	} {
		if !strings.Contains(taskAgent, required) {
			t.Fatalf("installer task agent does not contain %q:\n%s", required, taskAgent)
		}
	}
}

func TestInstallerGeneratedProjectLoadsStarterSkillAndTaskAgent(t *testing.T) {
	t.Setenv("API_KEY", "test-key")
	installer := readInstaller(t)
	root := t.TempDir()
	for path, marker := range map[string]string{
		".agentcli/config.yaml":                    `cat >"$target/.agentcli/config.yaml" <<'EOF'`,
		".agentcli/MAIN.md":                        `cat >"$target/.agentcli/MAIN.md" <<'EOF'`,
		".agentcli/skill/interview/SKILL.md":       `cat >"$target/.agentcli/skill/interview/SKILL.md" <<'EOF'`,
		".agentcli/agent/researcher/researcher.md": `cat >"$target/.agentcli/agent/researcher/researcher.md" <<'EOF'`,
	} {
		content := strings.TrimPrefix(installerHeredoc(t, installer, marker), "\n")
		writeTestFile(t, filepath.Join(root, path), content)
	}

	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	main := project.mainAgentDefinition()
	if len(main.Skills) != 1 || main.Skills[0] != "interview" {
		t.Fatalf("main skills = %v", main.Skills)
	}
	definitions := project.subagentDefinitions()
	if len(definitions) != 1 || definitions[0].Name != "researcher" {
		t.Fatalf("task-agent definitions = %#v", definitions)
	}
}

func TestInstallerRunsInteractiveTerminalWithoutArgumentMode(t *testing.T) {
	installer := readInstaller(t)
	for _, forbidden := range []string{
		`"strings"`,
		"initialPrompt",
		"agentcli.WithNonInteractive",
		"agentcli.WithTerminalInitialPrompt",
	} {
		if strings.Contains(installer, forbidden) {
			t.Fatalf("installer still contains argument-mode code %q", forbidden)
		}
	}
	if !strings.Contains(installer, "return agent.RunTerminal()") {
		t.Fatal("installer does not run the interactive terminal directly")
	}
}

func TestPlaygroundSetsDebugLogLevelInCode(t *testing.T) {
	content, err := os.ReadFile("playground/terminal/main.go")
	if err != nil {
		t.Fatalf("read terminal playground: %v", err)
	}
	if !strings.Contains(string(content), "agentcli.WithLogLevel(agentcli.LevelDebug)") {
		t.Fatal("terminal playground does not set debug logging in code")
	}
}

func TestExampleConfigMatchesMainAgent(t *testing.T) {
	t.Setenv("API_KEY", "test-key")
	root := t.TempDir()
	for source, destination := range map[string]string{
		".agentcli/config.example.yaml":            ".agentcli/config.yaml",
		".agentcli/MAIN.md":                        ".agentcli/MAIN.md",
		".agentcli/skill/interview/SKILL.md":       ".agentcli/skill/interview/SKILL.md",
		".agentcli/agent/researcher/researcher.md": ".agentcli/agent/researcher/researcher.md",
	} {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read example project file %s: %v", source, err)
		}
		writeTestFile(t, filepath.Join(root, destination), string(content))
	}

	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if project.providerName != "openai" || project.modelName != "qwen3.6-35b" {
		t.Fatalf("example project = %q/%q", project.providerName, project.modelName)
	}
	main := project.mainAgentDefinition()
	if len(main.Skills) != 1 || main.Skills[0] != "interview" {
		t.Fatalf("example main skills = %v", main.Skills)
	}
	definitions := project.subagentDefinitions()
	if len(definitions) != 1 || definitions[0].Name != "researcher" {
		t.Fatalf("example task-agent definitions = %#v", definitions)
	}
}

func readInstaller(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	return string(content)
}

func installerHeredoc(t *testing.T, installer, marker string) string {
	t.Helper()
	start := strings.Index(installer, marker)
	if start < 0 {
		t.Fatalf("installer does not contain heredoc marker %q", marker)
	}
	section := installer[start+len(marker):]
	end := strings.Index(section, "\nEOF")
	if end < 0 {
		t.Fatalf("installer heredoc %q is not terminated", marker)
	}
	return section[:end]
}
