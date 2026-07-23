package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/toolexecution"
)

const (
	defaultGlobResults = 100
	maximumGlobResults = 500
	defaultReadLines   = 400
	maximumReadLines   = 2000
	maximumReadBytes   = 256 << 10
	maximumLineBytes   = 1 << 20
)

var errSensitiveProjectFile = errors.New("sensitive project file is not available to model tools")

type projectToolScope struct{ root string }

func newGlobTool(root string) toolexecution.Tool {
	scope := mustProjectToolScope(root)
	return toolexecution.Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        "glob",
			Description: "Find project files using a relative glob pattern. Supports ** for recursive matching. Results are capped at 500 files; request a narrower pattern when truncated. Never follows directory symlinks and omits sensitive files.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Project-relative glob such as **/*.go"},"max_results":{"type":"integer","minimum":1,"maximum":500,"description":"Maximum paths to return; defaults to 100"}},"required":["pattern"],"additionalProperties":false}`),
		},
		Handler: scope.glob,
		Permission: toolexecution.StaticPermission(toolexecution.PermissionConfig{
			Actions: []permission.Action{permission.FilesystemRead}, Risk: permission.RiskLow,
			Reason: "Searches file names only within the configured project root.",
		}),
	}
}

func newReadTool(root string) toolexecution.Tool {
	scope := mustProjectToolScope(root)
	return toolexecution.Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        "read",
			Description: "Read a UTF-8 text file inside the project. Reads at most 2,000 lines and 256 KiB per call. If truncated, use next_offset to continue from the next line. Paths outside the project and sensitive files are denied.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Project-relative file path"},"offset":{"type":"integer","minimum":1,"description":"First 1-based line to return; defaults to 1"},"limit":{"type":"integer","minimum":1,"maximum":2000,"description":"Maximum lines to return; defaults to 400, maximum 2000"}},"required":["path"],"additionalProperties":false}`),
		},
		Handler: scope.read,
		Permission: toolexecution.StaticPermission(toolexecution.PermissionConfig{
			Actions: []permission.Action{permission.FilesystemRead}, Risk: permission.RiskLow,
			Reason: "Reads a bounded text range only from within the configured project root.",
		}),
	}
}

func mustProjectToolScope(root string) projectToolScope {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return projectToolScope{root: filepath.Clean(root)}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return projectToolScope{root: filepath.Clean(absolute)}
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return projectToolScope{root: filepath.Clean(resolved)}
	}
	return projectToolScope{root: filepath.Clean(resolved)}
}

func (scope projectToolScope) glob(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Pattern    string `json:"pattern"`
		MaxResults int    `json:"max_results"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return nil, err
	}
	pattern, err := validateGlob(input.Pattern)
	if err != nil {
		return nil, err
	}
	limit := input.MaxResults
	if limit == 0 {
		limit = defaultGlobResults
	}
	if limit < 1 || limit > maximumGlobResults {
		return nil, fmt.Errorf("max_results must be between 1 and %d", maximumGlobResults)
	}

	matches := make([]string, 0, min(limit, 64))
	truncated := false
	err = filepath.WalkDir(scope.root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(scope.root, filePath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative == ".git" || isSensitiveProjectPath(relative) {
				return filepath.SkipDir
			}
			return nil
		}
		if isSensitiveProjectPath(relative) {
			return nil
		}
		matched, err := matchGlob(pattern, relative)
		if err != nil {
			return err
		}
		if !matched {
			return nil
		}
		if len(matches) == limit {
			truncated = true
			return fs.SkipAll
		}
		matches = append(matches, relative)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("glob project files: %w", err)
	}
	sort.Strings(matches)
	return json.Marshal(struct {
		Pattern   string   `json:"pattern"`
		Files     []string `json:"files"`
		Truncated bool     `json:"truncated"`
	}{Pattern: pattern, Files: matches, Truncated: truncated})
}

func (scope projectToolScope) read(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return nil, err
	}
	offset := input.Offset
	if offset == 0 {
		offset = 1
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultReadLines
	}
	if offset < 1 {
		return nil, errors.New("offset must be at least 1")
	}
	if limit < 1 || limit > maximumReadLines {
		return nil, fmt.Errorf("limit must be between 1 and %d", maximumReadLines)
	}
	filePath, relative, err := scope.resolveFile(input.Path)
	if err != nil {
		return nil, err
	}
	if isSensitiveProjectPath(relative) {
		return nil, fmt.Errorf("read %q denied: %w", relative, errSensitiveProjectFile)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", relative, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maximumLineBytes)
	var content strings.Builder
	lineNumber, returned := 0, 0
	truncated := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lineNumber++
		if lineNumber < offset {
			continue
		}
		line := scanner.Text()
		if !utf8.ValidString(line) {
			return nil, fmt.Errorf("read %q: file is not valid UTF-8 text", relative)
		}
		if returned == limit || content.Len()+len(line)+1 > maximumReadBytes {
			if returned == 0 && len(line)+1 > maximumReadBytes {
				return nil, fmt.Errorf("read %q: line %d exceeds the maximum output size", relative, lineNumber)
			}
			truncated = true
			break
		}
		content.WriteString(line)
		content.WriteByte('\n')
		returned++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %q: %w", relative, err)
	}
	endLine := offset + returned - 1
	if returned == 0 {
		endLine = 0
	}
	nextOffset := 0
	if truncated {
		nextOffset = offset + returned
	}
	return json.Marshal(struct {
		Path       string `json:"path"`
		Content    string `json:"content"`
		StartLine  int    `json:"start_line"`
		EndLine    int    `json:"end_line"`
		NextOffset int    `json:"next_offset,omitempty"`
		Truncated  bool   `json:"truncated"`
	}{Path: relative, Content: content.String(), StartLine: offset, EndLine: endLine, NextOffset: nextOffset, Truncated: truncated})
}

func (scope projectToolScope) resolveFile(name string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) {
		return "", "", errors.New("path must be relative to the project root")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(scope.root, filepath.Clean(name)))
	if err != nil {
		return "", "", fmt.Errorf("resolve %q: %w", name, err)
	}
	relative, err := filepath.Rel(scope.root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q resolves outside the project root", name)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", fmt.Errorf("inspect %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("path %q is not a regular file", name)
	}
	return resolved, filepath.ToSlash(relative), nil
}

func decodeArguments(arguments json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode tool arguments: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode tool arguments: multiple JSON values")
		}
		return fmt.Errorf("decode tool arguments: %w", err)
	}
	return nil
}

func validateGlob(pattern string) (string, error) {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" || strings.HasPrefix(pattern, "/") || filepath.IsAbs(pattern) {
		return "", errors.New("pattern must be relative to the project root")
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == ".." {
			return "", errors.New("pattern cannot traverse outside the project root")
		}
		if segment != "**" {
			if _, err := path.Match(segment, "validate"); err != nil {
				return "", fmt.Errorf("invalid glob pattern: %w", err)
			}
		}
	}
	return pattern, nil
}

func matchGlob(pattern, name string) (bool, error) {
	patterns, names := strings.Split(pattern, "/"), strings.Split(name, "/")
	type state struct{ pattern, name int }
	memo, seen := make(map[state]bool), make(map[state]bool)
	var match func(int, int) (bool, error)
	match = func(patternIndex, nameIndex int) (bool, error) {
		key := state{patternIndex, nameIndex}
		if seen[key] {
			return memo[key], nil
		}
		seen[key] = true
		if patternIndex == len(patterns) {
			memo[key] = nameIndex == len(names)
			return memo[key], nil
		}
		if patterns[patternIndex] == "**" {
			matched, err := match(patternIndex+1, nameIndex)
			if err != nil || matched {
				memo[key] = matched
				return matched, err
			}
			if nameIndex < len(names) {
				matched, err = match(patternIndex, nameIndex+1)
				memo[key] = matched
				return matched, err
			}
			return false, nil
		}
		if nameIndex == len(names) {
			return false, nil
		}
		matched, err := path.Match(patterns[patternIndex], names[nameIndex])
		if err != nil || !matched {
			return false, err
		}
		matched, err = match(patternIndex+1, nameIndex+1)
		memo[key] = matched
		return matched, err
	}
	return match(0, 0)
}

func isSensitiveProjectPath(name string) bool {
	normalized := strings.ToLower(strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), "./"))
	segments := strings.Split(normalized, "/")
	for _, segment := range segments {
		switch segment {
		case ".ssh", ".gnupg", ".aws", ".azure", ".kube":
			return true
		}
	}
	if normalized == ".agentcli/config.yaml" || normalized == ".agentcli/config.yml" {
		return true
	}
	base := segments[len(segments)-1]
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return base != ".env.example" && base != ".env.sample" && base != ".env.template"
	}
	switch base {
	case ".netrc", ".npmrc", ".pypirc", ".git-credentials", "credentials", "credentials.json", "secret.json", "secrets.json", "id_rsa", "id_ed25519":
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore":
		return true
	}
	return false
}
