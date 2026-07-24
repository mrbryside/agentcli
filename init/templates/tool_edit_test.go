package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli"
)

func TestEditToolUsesDoubleAdmissionMetadata(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "example.go")
	if err := os.WriteFile(path, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	tool := newEditTool(directory)
	if tool.Definition.Name != "edit" || tool.Permission == nil || tool.Confirmation == nil {
		t.Fatalf("edit tool metadata = %#v", tool)
	}
	arguments := json.RawMessage(`{"path":"example.go","old_string":"before","new_string":"after"}`)
	permission, err := tool.Permission(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if len(permission.Actions) != 1 || permission.Actions[0] != agentcli.FilesystemWrite || permission.Risk != agentcli.RiskHigh {
		t.Fatalf("permission = %#v", permission)
	}
	confirmation, err := tool.Confirmation(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if confirmation.Title != "Confirm exact file edit" || !strings.Contains(confirmation.Details, "example.go") {
		t.Fatalf("confirmation = %#v", confirmation)
	}
	if strings.Contains(confirmation.Details, "before\n") || strings.Contains(confirmation.Details, "after\n") {
		t.Fatalf("confirmation leaked raw content: %q", confirmation.Details)
	}
}

func TestEditToolReplacesOneExactOccurrence(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "example.go")
	if err := os.WriteFile(path, []byte("one\nold\nthree\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	output, err := newEditTool(directory).Handler(context.Background(), json.RawMessage(`{"path":"example.go","old_string":"old","new_string":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "one\nnew\nthree\n" {
		t.Fatalf("content = %q", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	var result struct {
		Path         string `json:"path"`
		Changed      bool   `json:"changed"`
		Replacements int    `json:"replacements"`
		OldStartLine int    `json:"old_start_line"`
		OldEndLine   int    `json:"old_end_line"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != "example.go" || !result.Changed || result.Replacements != 1 || result.OldStartLine != 2 || result.OldEndLine != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestEditToolRejectsAmbiguousAndAllowsEmptyReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "example.txt")
	if err := os.WriteFile(path, []byte("old old"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newEditTool(directory)
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"path":"example.txt","old_string":"old","new_string":"new"}`)); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous error = %v", err)
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"path":"example.txt","old_string":"old old","new_string":""}`)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatalf("empty replacement content = %q", content)
	}
}

func TestEditToolRejectsSymlinkAndSensitivePath(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := newEditTool(directory)
	arguments := json.RawMessage(`{"path":"link.txt","old_string":"old","new_string":"new"}`)
	if _, err := tool.Permission(arguments); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if _, err := tool.Permission(json.RawMessage(`{"path":".env","old_string":"old","new_string":"new"}`)); err == nil {
		t.Fatal("expected sensitive path rejection")
	}
}

func TestUniqueEditMatchCountsOverlaps(t *testing.T) {
	if index, count := uniqueEditMatch([]byte("aaa"), []byte("aa")); index != 0 || count != 2 {
		t.Fatalf("overlap = (%d, %d)", index, count)
	}
}
