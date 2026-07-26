package agentcli

import (
	"os"
	"strings"
	"testing"
)

func TestInstallerMainPromptKeepsUserFacingOutputInsideTriggerTool(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	installer := string(content)
	for _, required := range []string{
		"## Answer or communicate with user",
		"`report_discord` is the only user-visible response channel",
		"Leave normal assistant",
		"Never write that text first and call the tool afterward",
		"Keep the complete message at or below 1800 Unicode characters",
		"Summarize",
		`"will report back"`,
		"For completed work, report the findings directly",
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer MAIN.md prompt does not contain %q", required)
		}
	}
}

func TestInstallerDefinesSeparateGuardrailsProvider(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	installer := string(content)
	for _, required := range []string{
		"  guardrails:",
		"api_key: ${GUARDRAILS_API_KEY}",
		"request_timeout: 30s",
		"export GUARDRAILS_API_KEY=...",
		"Replace replace-guard-model in tool_report_discord.go",
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer guard provider config does not contain %q", required)
		}
	}
}

func TestInstallerEnablesCompactionWithStarterPlaceholders(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	installer := string(content)
	const required = `compaction:
  auto: true
  provider: replace-provider
  model: replace-model`
	if !strings.Contains(installer, required) {
		t.Fatalf("installer compaction config does not contain:\n%s", required)
	}
}

func TestInstallerFallbackVersionTracksCurrentRelease(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	if !strings.Contains(string(content), "agentcli_fallback_version=v0.0.53") {
		t.Fatal("installer fallback version does not track v0.0.53")
	}
}

func TestInstallerIncludesProviderMetadataDefaults(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	const required = `request_timeout: 2m
    models:
      replace-model:
        context_window_tokens: 122880 # Remove when provider discovery is available.
        max_output_tokens: 66560
        # reasoning: false # Optional Qwen-compatible shorthand.
        # extra_body: # Optional model-specific top-level request JSON.
        #   thinking:
        #     type: disabled`
	if !strings.Contains(string(content), required) {
		t.Fatalf("installer provider metadata does not contain:\n%s", required)
	}
}

func TestInstallerIncludesDisabledLangfuseExample(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	const required = `# observability:
#   langfuse:
#     enabled: true
#     base_url: ${LANGFUSE_BASE_URL} # Defaults to https://cloud.langfuse.com
#     public_key: ${LANGFUSE_PUBLIC_KEY}
#     secret_key: ${LANGFUSE_SECRET_KEY}
#     environment: development
#     service_name: agentcli
#     release: ${APP_VERSION}
#     sample_rate: 1.0
#     capture:
#       input: true
#       output: true
#       reasoning: false`
	installer := string(content)
	if !strings.Contains(installer, required) {
		t.Fatalf("installer does not contain the disabled Langfuse example:\n%s", required)
	}
	if strings.Contains(installer, "\nobservability:\n") {
		t.Fatal("installer enables observability instead of leaving the example commented")
	}
}

func TestInstallerIncludesDisabledRuntimeLoggingExample(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	const required = `# transcript lifecycle logs.
# Omit this mapping to disable console logging.
# logging:
#   enabled: true
#   level: info # debug, info, warn, or error`
	installer := string(content)
	if !strings.Contains(installer, required) {
		t.Fatalf("installer does not contain the disabled logging example:\n%s", required)
	}
	if strings.Contains(installer, "\nlogging:\n") {
		t.Fatal("installer enables runtime logging instead of leaving the example commented")
	}
}

func TestInstallerIncludesCurrentProjectOptions(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	installer := string(content)
	for _, required := range []string{
		"permission_mode: criticalOnly\nmax_subagents: 4",
		"  # openrouter:\n  #   type: openai\n  #   url: https://openrouter.ai/api/v1",
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer current project config does not contain:\n%s", required)
		}
	}
}

func TestInstallerMatchesPlaygroundOneShotBehavior(t *testing.T) {
	installerContent, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	playgroundContent, err := os.ReadFile("playground/terminal/main.go")
	if err != nil {
		t.Fatalf("read terminal playground: %v", err)
	}
	const required = "agentcli.WithNonInteractive(initialPrompt != \"\")"
	if !strings.Contains(string(installerContent), required) {
		t.Fatalf("installer main.go does not contain %q", required)
	}
	if !strings.Contains(string(playgroundContent), required) {
		t.Fatalf("terminal playground does not contain %q", required)
	}
}

func TestInstallerMainAllowsOnlyReportDiscord(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	const marker = `cat >"$target/.agentcli/MAIN.md" <<'EOF'`
	section := string(content)
	start := strings.Index(section, marker)
	if start < 0 {
		t.Fatalf("installer does not contain MAIN.md heredoc")
	}
	section = section[start+len(marker):]
	end := strings.Index(section, "\nEOF")
	if end < 0 {
		t.Fatalf("installer MAIN.md heredoc is not terminated")
	}
	mainDefinition := section[:end]
	if !strings.Contains(mainDefinition, "tools:\n  - report_discord\n---") {
		t.Fatalf("installer main tool allowlist is not report_discord-only:\n%s", mainDefinition)
	}
	for _, forbidden := range []string{"  - glob\n", "  - read\n", "  - edit\n"} {
		if strings.Contains(mainDefinition, forbidden) {
			t.Fatalf("installer main tool allowlist contains %q", strings.TrimSpace(forbidden))
		}
	}
}
