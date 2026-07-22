package bashsecure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyAllowsStaticProjectAndTempCommands(t *testing.T) {
	project := t.TempDir()
	temporary := t.TempDir()
	scope, err := NewScope(project, temporary)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(project, "abc.sh")
	if err := os.WriteFile(script, []byte("printf ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := []string{
		"go test ./...",
		"pwd && printf 'ok'",
		"grep -R needle . | head -n 2",
		"printf result > output.txt",
		"printf result > " + filepath.Join(temporary, "harness-result.txt"),
		"chmod +x abc.sh",
		"bash abc.sh",
		"./abc.sh",
		"curl -fL https://example.com/path?q=value",
		"curl --output result.json https://example.com/data.json",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if err := Validate(command, scope); err != nil {
				t.Fatalf("command denied: %v", err)
			}
		})
	}
}

func TestPolicyDeniesDangerousDynamicAndOutsideCommands(t *testing.T) {
	scope := testScope(t)
	tests := map[string]string{
		"rm -rf .":                       "file deletion",
		"sudo ls":                        "privilege escalation",
		"kill -9 123":                    "process termination",
		"git reset --hard":               "discards worktree",
		"find . -delete":                 "find action",
		"cat /etc/passwd":                "outside",
		"cd ../../..":                    "outside",
		"printf bad > /Users/other/file": "outside",
		"cat $HOME/.ssh/id_ed25519":      "dynamic shell expansion",
		"echo $(cat /etc/passwd)":        "dynamic shell expansion",
		"sh -c 'cat /etc/passwd'":        "nested shell flags",
		"bash":                           "requires a static",
		"source ./project-script.sh":     "nested shell code",
		"xargs rm":                       "command indirection",
		"nice rm -rf .":                  "command indirection",
		"curl file:///etc/passwd":        "invalid network URL",
		"curl ftp://example.com/file":    "URL scheme",
	}
	for command, want := range tests {
		t.Run(command, func(t *testing.T) {
			err := Validate(command, scope)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want substring %q", err, want)
			}
		})
	}
}
