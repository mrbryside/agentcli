package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGlobToolFindsRecursiveProjectFilesAndStaysInScope(t *testing.T) {
	root := t.TempDir()
	writeProjectToolFixture(t, filepath.Join(root, "main.go"), "package main\n")
	writeProjectToolFixture(t, filepath.Join(root, "nested", "worker.go"), "package nested\n")
	writeProjectToolFixture(t, filepath.Join(root, "nested", "notes.md"), "notes\n")
	writeProjectToolFixture(t, filepath.Join(root, ".git", "ignored.go"), "ignored\n")
	tool := newGlobTool(root)

	output, err := tool.Handler(context.Background(), json.RawMessage(`{"pattern":"**/*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Files     []string `json:"files"`
		Truncated bool     `json:"truncated"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Files, []string{"main.go", "nested/worker.go"}) || result.Truncated {
		t.Fatalf("glob result = %s", output)
	}

	limited, err := tool.Handler(context.Background(), json.RawMessage(`{"pattern":"**/*","max_results":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(limited, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || !result.Truncated {
		t.Fatalf("limited glob result = %s", limited)
	}
	for _, arguments := range []string{
		`{"pattern":"../*.go"}`,
		`{"pattern":"/tmp/*.go"}`,
		`{"pattern":"["}`,
		`{"pattern":"*.go","unknown":true}`,
	} {
		if _, err := tool.Handler(context.Background(), json.RawMessage(arguments)); err == nil {
			t.Fatalf("glob accepted %s", arguments)
		}
	}
}

func TestReadToolReadsBoundedLinesAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeProjectToolFixture(t, filepath.Join(root, "notes.txt"), "one\ntwo\nthree\n")
	writeProjectToolFixture(t, outside, "secret\n")
	if err := os.Symlink(outside, filepath.Join(root, "outside-link.txt")); err != nil {
		t.Fatal(err)
	}
	tool := newReadTool(root)

	output, err := tool.Handler(context.Background(), json.RawMessage(`{"path":"notes.txt","offset":2,"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Path       string `json:"path"`
		Content    string `json:"content"`
		StartLine  int    `json:"start_line"`
		EndLine    int    `json:"end_line"`
		NextOffset int    `json:"next_offset"`
		Truncated  bool   `json:"truncated"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != "notes.txt" || result.Content != "two\n" || result.StartLine != 2 || result.EndLine != 2 || result.NextOffset != 3 || !result.Truncated {
		t.Fatalf("read result = %s", output)
	}

	for _, arguments := range []string{
		`{"path":"../outside.txt"}`,
		`{"path":"outside-link.txt"}`,
		`{"path":"notes.txt","offset":0,"limit":3000}`,
		`{"path":"notes.txt","unknown":true}`,
	} {
		if _, err := tool.Handler(context.Background(), json.RawMessage(arguments)); err == nil {
			t.Fatalf("read accepted %s", arguments)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Handler(canceled, json.RawMessage(`{"path":"notes.txt"}`)); err == nil {
		t.Fatal("read ignored canceled context")
	}
}

func TestProjectToolsDenySensitiveFilesBeforeModelAccess(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		".agentcli/config.yaml":         "providers: {openai: {api_key: live-secret}}\n",
		".agentcli/config.example.yaml": "providers: {openai: {api_key: replace-me}}\n",
		".env":                          "TOKEN=live-secret\n",
		".env.local":                    "TOKEN=live-secret\n",
		".env.example":                  "TOKEN=replace-me\n",
		"deploy/secrets.yaml":           "token: live-secret\n",
		"certs/service.key":             "private key material\n",
		".ssh/id_ed25519":               "private key material\n",
		"docs/notes.md":                 "safe notes\n",
	}
	for name, content := range fixtures {
		writeProjectToolFixture(t, filepath.Join(root, name), content)
	}

	read := newReadTool(root)
	for _, name := range []string{".agentcli/config.yaml", ".env", ".env.local", "deploy/secrets.yaml", "certs/service.key", ".ssh/id_ed25519"} {
		arguments, _ := json.Marshal(map[string]string{"path": name})
		if _, err := read.Handler(context.Background(), arguments); !errors.Is(err, errSensitiveProjectFile) {
			t.Fatalf("read %q error = %v, want sensitive-file denial", name, err)
		}
	}
	for _, name := range []string{".agentcli/config.example.yaml", ".env.example", "docs/notes.md"} {
		arguments, _ := json.Marshal(map[string]string{"path": name})
		if _, err := read.Handler(context.Background(), arguments); err != nil {
			t.Fatalf("read safe template %q: %v", name, err)
		}
	}

	output, err := newGlobTool(root).Handler(context.Background(), json.RawMessage(`{"pattern":"**/*"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{".agentcli/config.yaml", ".env", ".env.local", "deploy/secrets.yaml", "certs/service.key", ".ssh/id_ed25519"} {
		if slices.Contains(result.Files, hidden) {
			t.Fatalf("glob exposed sensitive path %q in %#v", hidden, result.Files)
		}
	}
	for _, visible := range []string{".agentcli/config.example.yaml", ".env.example", "docs/notes.md"} {
		if !slices.Contains(result.Files, visible) {
			t.Fatalf("glob omitted safe path %q from %#v", visible, result.Files)
		}
	}
}

func TestProjectGlobstarMatching(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "a/b/main.go", true},
		{"agentcli/**/test*.go", "agentcli/a/b/test_one.go", true},
		{"agentcli/*.go", "agentcli/a/main.go", false},
		{"**/*.go", "README.md", false},
	}
	for _, test := range tests {
		got, err := matchProjectGlob(test.pattern, test.name)
		if err != nil || got != test.want {
			t.Fatalf("matchProjectGlob(%q, %q) = (%v, %v), want %v", test.pattern, test.name, got, err, test.want)
		}
	}
}

func writeProjectToolFixture(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectReadRejectsInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "binary.txt")
	if err := os.WriteFile(name, []byte{0xff, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newReadTool(root).Handler(context.Background(), json.RawMessage(`{"path":"binary.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
}
