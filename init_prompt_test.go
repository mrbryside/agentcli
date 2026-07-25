package agentcli

import (
	"os"
	"strings"
	"testing"
)

func TestInstallerMainPromptKeepsUserFacingOutputInsideFinalizer(t *testing.T) {
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
	if !strings.Contains(string(content), "agentcli_fallback_version=v0.0.39") {
		t.Fatal("installer fallback version does not track v0.0.39")
	}
}

func TestInstallerIncludesProviderMetadataDefaults(t *testing.T) {
	content, err := os.ReadFile("init/install.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	const required = `request_timeout: 2m
    context_window_tokens: 122880 # 120k; remove these limits when provider discovery is available.
    max_output_tokens: 66560 # 65k`
	if !strings.Contains(string(content), required) {
		t.Fatalf("installer provider metadata does not contain:\n%s", required)
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
