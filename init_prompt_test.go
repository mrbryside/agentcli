package agentcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerGeneratesMinimalMainAgent(t *testing.T) {
	installer := readInstaller(t)
	mainDefinition := installerHeredoc(t, installer, `cat >"$target/.agentcli/MAIN.md" <<'EOF'`)

	const expected = `
---
provider: replace-provider
model: replace-model
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

func TestInstallerConfigOmitsLoggingObservabilityAndComments(t *testing.T) {
	config := installerHeredoc(t, readInstaller(t), `cat >"$target/.agentcli/config.yaml" <<'EOF'`)
	for _, forbidden := range []string{"logging:", "observability:", "langfuse:", "#"} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("installer config still contains %q:\n%s", forbidden, config)
		}
	}
	for _, required := range []string{
		"permission_mode: criticalOnly",
		"compaction:\n  auto: true\n  provider: replace-provider\n  model: replace-model",
		"providers:\n  replace-provider:",
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

func TestInstallerIncludesProviderMetadataDefaults(t *testing.T) {
	const required = `request_timeout: 2m
    models:
      replace-model:
        context_window_tokens: 122880
        max_output_tokens: 66560`
	if installer := readInstaller(t); !strings.Contains(installer, required) {
		t.Fatalf("installer provider metadata does not contain:\n%s", required)
	}
}

func TestInstallerMatchesPlaygroundOneShotBehavior(t *testing.T) {
	installer := readInstaller(t)
	playgroundContent, err := os.ReadFile("playground/terminal/main.go")
	if err != nil {
		t.Fatalf("read terminal playground: %v", err)
	}
	const required = "agentcli.WithNonInteractive(initialPrompt != \"\")"
	if !strings.Contains(installer, required) {
		t.Fatalf("installer main.go does not contain %q", required)
	}
	if !strings.Contains(string(playgroundContent), required) {
		t.Fatalf("terminal playground does not contain %q", required)
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
	config, err := os.ReadFile(".agentcli/config.example.yaml")
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	mainDefinition, err := os.ReadFile(".agentcli/MAIN.md")
	if err != nil {
		t.Fatalf("read main agent: %v", err)
	}
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), string(config))
	writeTestFile(t, filepath.Join(root, ".agentcli", "MAIN.md"), string(mainDefinition))

	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if project.providerName != "openai" || project.modelName != "qwen3.6-35b" {
		t.Fatalf("example project = %q/%q", project.providerName, project.modelName)
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
