package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mrbryside/agentcli"
)

const (
	defaultGlobResults = 100
	maximumGlobResults = 500
)

func newGlobTool(root string) agentcli.Tool {
	scope := mustProjectToolScope(root)
	return agentcli.Tool{
		Definition: agentcli.ToolDefinition{
			Name:        "glob",
			Description: "Find project files using a relative glob pattern. Supports ** for recursive matching. Results are capped at 500 files; request a narrower pattern when truncated. Never follows directory symlinks and omits sensitive files.",
			InputSchema: agentcli.ObjectSchema(struct {
				Pattern    agentcli.ToolParameter
				MaxResults agentcli.ToolParameter
			}{
				Pattern:    agentcli.StringParameter("Project-relative glob such as **/*.go").Required().MinLength(1),
				MaxResults: agentcli.IntegerParameter("Maximum paths to return; defaults to 100").Minimum(1).Maximum(500),
			}),
		},
		Handler: scope.glob,
		Permission: agentcli.ToolStaticPermission(agentcli.ToolPermissionConfig{
			Actions: []agentcli.PermissionAction{agentcli.FilesystemRead}, Risk: agentcli.RiskLow,
			Reason: "Searches file names only within the configured project root.",
		}),
	}
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
