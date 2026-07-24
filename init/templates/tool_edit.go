package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mrbryside/agentcli"
)

const (
	maximumEditPathBytes    = 1024
	maximumEditSnippetBytes = 64 << 10
	maximumEditSnippetLines = 400
	maximumEditableBytes    = 256 << 10
	maximumEditableLines    = 2000
	editWriteChunkBytes     = 32 << 10
	editPreviewRunes        = 80
)

type editArguments struct {
	Path      *string `json:"path"`
	OldString *string `json:"old_string"`
	NewString *string `json:"new_string"`
}

type normalizedEdit struct {
	Path, OldString, NewString string
	OldBytes, NewBytes         int
	OldLines, NewLines         int
}

func newEditTool(root string) agentcli.Tool {
	scope := mustProjectToolScope(root)
	return agentcli.Tool{
		Definition: agentcli.ToolDefinition{
			Name:        "edit",
			Description: "Replace one exact, unique text occurrence in an existing UTF-8 text file inside the project. The file must stay within the project and is never created, deleted, or followed through symlinks. Every invocation requires filesystem-write permission and a separate Yes/No confirmation.",
			InputSchema: agentcli.ObjectSchema(struct {
				Path      agentcli.ToolParameter
				OldString agentcli.ToolParameter
				NewString agentcli.ToolParameter
			}{
				Path:      agentcli.StringParameter("Project-relative path of an existing regular UTF-8 text file").Required().MinLength(1).MaxLength(maximumEditPathBytes),
				OldString: agentcli.StringParameter("Exact existing text to replace; it must occur exactly once").Required().MinLength(1).MaxLength(maximumEditSnippetBytes),
				NewString: agentcli.StringParameter("Replacement text; may be empty to delete the matched text").Required().MaxLength(maximumEditSnippetBytes),
			}),
		},
		Handler:      scope.edit,
		Permission:   scope.describeEditPermission,
		Confirmation: scope.describeEditConfirmation,
	}
}

func (scope projectToolScope) normalizeEdit(arguments json.RawMessage) (normalizedEdit, error) {
	var input editArguments
	if err := agentcli.DecodeArguments(arguments, &input); err != nil {
		return normalizedEdit{}, err
	}
	if input.Path == nil || input.OldString == nil || input.NewString == nil {
		return normalizedEdit{}, errors.New("edit requires path, old_string, and new_string")
	}
	path, err := normalizeEditPath(scope, *input.Path)
	if err != nil {
		return normalizedEdit{}, err
	}
	oldString, oldBytes, oldLines, err := validateEditText("old_string", *input.OldString, maximumEditSnippetBytes, maximumEditSnippetLines, false)
	if err != nil {
		return normalizedEdit{}, err
	}
	newString, newBytes, newLines, err := validateEditText("new_string", *input.NewString, maximumEditSnippetBytes, maximumEditSnippetLines, true)
	if err != nil {
		return normalizedEdit{}, err
	}
	if oldString == newString {
		return normalizedEdit{}, errors.New("old_string and new_string are identical")
	}
	if _, _, _, err := scope.resolveEditableFile(path); err != nil {
		return normalizedEdit{}, err
	}
	return normalizedEdit{Path: path, OldString: oldString, NewString: newString, OldBytes: oldBytes, NewBytes: newBytes, OldLines: oldLines, NewLines: newLines}, nil
}

func normalizeEditPath(scope projectToolScope, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maximumEditPathBytes || filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("edit path must be a short relative path")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("edit path cannot contain control characters")
		}
	}
	clean := filepath.Clean(value)
	if clean == "." {
		return "", errors.New("edit path must name an existing file")
	}
	joined := filepath.Join(scope.root, clean)
	relative, err := filepath.Rel(scope.root, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("edit path must stay inside the project")
	}
	relative = filepath.ToSlash(relative)
	if isProtectedEditPath(relative) {
		return "", fmt.Errorf("edit %q denied: %w", relative, errSensitiveProjectFile)
	}
	return relative, nil
}

func validateEditText(name, value string, maximumBytes, maximumLines int, allowEmpty bool) (string, int, int, error) {
	if !allowEmpty && value == "" {
		return "", 0, 0, fmt.Errorf("%s must not be empty", name)
	}
	if !utf8.ValidString(value) {
		return "", 0, 0, fmt.Errorf("%s must be valid UTF-8", name)
	}
	if len(value) > maximumBytes {
		return "", 0, 0, fmt.Errorf("%s exceeds %d bytes", name, maximumBytes)
	}
	for _, r := range value {
		if r == 0 || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
			return "", 0, 0, fmt.Errorf("%s contains unsupported control characters", name)
		}
	}
	lines := editLineCount(value)
	if lines > maximumLines {
		return "", 0, 0, fmt.Errorf("%s exceeds %d lines", name, maximumLines)
	}
	return value, len(value), lines, nil
}

func editLineCount(value string) int {
	if value == "" {
		return 0
	}
	lines := strings.Count(value, "\n")
	if !strings.HasSuffix(value, "\n") {
		lines++
	}
	return lines
}

func isProtectedEditPath(name string) bool {
	normalized := strings.ToLower(strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), "./"))
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".git" {
			return true
		}
	}
	return isSensitiveProjectPath(normalized)
}

func (scope projectToolScope) resolveEditableFile(name string) (string, string, os.FileInfo, error) {
	name, err := normalizeEditPathWithoutTarget(scope, name)
	if err != nil {
		return "", "", nil, err
	}
	path := filepath.Join(scope.root, filepath.FromSlash(name))
	relative, err := filepath.Rel(scope.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", nil, errors.New("edit path must stay inside the project")
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	current := scope.root
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return "", "", nil, fmt.Errorf("inspect %q: %w", name, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", nil, fmt.Errorf("edit %q denied: symlinks are not followed", name)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", "", nil, fmt.Errorf("edit %q has a non-directory parent", name)
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return "", "", nil, fmt.Errorf("edit %q is not a regular file", name)
			}
			return current, filepath.ToSlash(relative), info, nil
		}
	}
	return "", "", nil, errors.New("edit path must name a file")
}

func normalizeEditPathWithoutTarget(scope projectToolScope, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maximumEditPathBytes || filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("edit path must be a short relative path")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("edit path cannot contain control characters")
		}
	}
	clean := filepath.Clean(value)
	if clean == "." {
		return "", errors.New("edit path must name an existing file")
	}
	relative, err := filepath.Rel(scope.root, filepath.Join(scope.root, clean))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("edit path must stay inside the project")
	}
	relative = filepath.ToSlash(relative)
	if isProtectedEditPath(relative) {
		return "", fmt.Errorf("edit %q denied: %w", relative, errSensitiveProjectFile)
	}
	return relative, nil
}

func (scope projectToolScope) describeEditPermission(arguments json.RawMessage) (agentcli.ToolPermissionDescription, error) {
	normalized, err := scope.normalizeEdit(arguments)
	if err != nil {
		return agentcli.ToolPermissionDescription{}, err
	}
	return agentcli.ToolPermissionDescription{Actions: []agentcli.PermissionAction{agentcli.FilesystemWrite}, Risk: agentcli.RiskHigh, Reason: "Replaces one unique exact text occurrence in an existing project file.", Details: editDetails(normalized)}, nil
}

func (scope projectToolScope) describeEditConfirmation(arguments json.RawMessage) (agentcli.ToolConfirmationDescription, error) {
	normalized, err := scope.normalizeEdit(arguments)
	if err != nil {
		return agentcli.ToolConfirmationDescription{}, err
	}
	return agentcli.ToolConfirmationDescription{Title: "Confirm exact file edit", Message: "Replace one exact text occurrence in this project file?", Details: editDetails(normalized)}, nil
}

func editDetails(value normalizedEdit) string {
	oldHash := sha256.Sum256([]byte(value.OldString))
	newHash := sha256.Sum256([]byte(value.NewString))
	newSummary := fmt.Sprintf("%d bytes, %d lines, SHA-256 %s, preview %s", value.NewBytes, value.NewLines, hex.EncodeToString(newHash[:])[:12], editPreview(value.NewString))
	if value.NewString == "" {
		newSummary = "empty (deletes the matched text)"
	}
	return fmt.Sprintf("Path: %s\nOld text: %d bytes, %d lines, SHA-256 %s, preview %s\nNew text: %s", value.Path, value.OldBytes, value.OldLines, hex.EncodeToString(oldHash[:])[:12], editPreview(value.OldString), newSummary)
}

func editPreview(value string) string {
	runes := []rune(value)
	if len(runes) > editPreviewRunes {
		runes = runes[:editPreviewRunes]
		return strconv.QuoteToASCII(string(runes)) + "…"
	}
	return strconv.QuoteToASCII(value)
}

func (scope projectToolScope) edit(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	input, err := scope.normalizeEdit(arguments)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, relative, originalInfo, err := scope.resolveEditableFile(input.Path)
	if err != nil {
		return nil, err
	}
	original, info, err := readEditableFile(target, originalInfo)
	if err != nil {
		return nil, fmt.Errorf("edit %q: %w", relative, err)
	}
	match, count := uniqueEditMatch(original, []byte(input.OldString))
	if count == 0 {
		return nil, fmt.Errorf("edit %q: old_string was not found", relative)
	}
	if count > 1 {
		return nil, fmt.Errorf("edit %q: old_string is ambiguous; found more than one occurrence", relative)
	}
	replacementSize := len(original) - len(input.OldString) + len(input.NewString)
	if replacementSize > maximumEditableBytes {
		return nil, fmt.Errorf("edit %q: replacement would exceed %d bytes", relative, maximumEditableBytes)
	}
	replacement := make([]byte, 0, replacementSize)
	replacement = append(replacement, original[:match]...)
	replacement = append(replacement, input.NewString...)
	replacement = append(replacement, original[match+len(input.OldString):]...)
	if _, _, _, err := validateEditText("replacement", string(replacement), maximumEditableBytes, maximumEditableLines, true); err != nil {
		return nil, fmt.Errorf("edit %q: %w", relative, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targetAgain, _, infoAgain, err := scope.resolveEditableFile(relative)
	if err != nil || targetAgain != target || !os.SameFile(info, infoAgain) {
		return nil, fmt.Errorf("edit %q: file changed during edit; retry after reading it again", relative)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".agentcli-edit-*")
	if err != nil {
		return nil, fmt.Errorf("edit %q: create temporary file: %w", relative, err)
	}
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporary.Name())
		}
	}()
	for offset := 0; offset < len(replacement); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(offset+editWriteChunkBytes, len(replacement))
		written, writeErr := temporary.Write(replacement[offset:end])
		if writeErr != nil {
			return nil, fmt.Errorf("edit %q: write temporary file: %w", relative, writeErr)
		}
		if written != end-offset {
			return nil, io.ErrShortWrite
		}
		offset = end
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("edit %q: preserve file mode: %w", relative, err)
	}
	if err := temporary.Sync(); err != nil {
		return nil, fmt.Errorf("edit %q: sync temporary file: %w", relative, err)
	}
	if err := temporary.Close(); err != nil {
		return nil, fmt.Errorf("edit %q: close temporary file: %w", relative, err)
	}
	finalTarget, _, finalInfo, err := scope.resolveEditableFile(relative)
	if err != nil || finalTarget != target || !os.SameFile(info, finalInfo) {
		return nil, fmt.Errorf("edit %q: file changed during edit; retry after reading it again", relative)
	}
	current, currentInfo, err := readEditableFile(target, finalInfo)
	if err != nil || !os.SameFile(info, currentInfo) || !bytes.Equal(current, original) {
		return nil, fmt.Errorf("edit %q: file changed during edit; retry after reading it again", relative)
	}
	if err := os.Rename(temporary.Name(), target); err != nil {
		return nil, fmt.Errorf("edit %q: replace file atomically: %w", relative, err)
	}
	committed = true
	return json.Marshal(struct {
		Path         string `json:"path"`
		Changed      bool   `json:"changed"`
		Replacements int    `json:"replacements"`
		OldStartLine int    `json:"old_start_line"`
		OldEndLine   int    `json:"old_end_line"`
		Bytes        int    `json:"bytes"`
		Lines        int    `json:"lines"`
	}{Path: relative, Changed: true, Replacements: 1, OldStartLine: editLineAtOffset(original, match), OldEndLine: editLineAtOffset(original, match+len(input.OldString)-1), Bytes: len(replacement), Lines: editLineCount(string(replacement))})
}

func readEditableFile(path string, expected os.FileInfo) ([]byte, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(expected, info) {
		return nil, nil, errors.New("file changed during edit")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumEditableBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(content) > maximumEditableBytes {
		return nil, nil, fmt.Errorf("file exceeds %d bytes", maximumEditableBytes)
	}
	if _, _, _, err := validateEditText("file", string(content), maximumEditableBytes, maximumEditableLines, true); err != nil {
		return nil, nil, err
	}
	return content, info, nil
}

func uniqueEditMatch(content, old []byte) (int, int) {
	first, count := -1, 0
	for start := 0; start <= len(content)-len(old); {
		relative := bytes.Index(content[start:], old)
		if relative < 0 {
			break
		}
		found := start + relative
		count++
		if count == 1 {
			first = found
		}
		if count == 2 {
			return first, count
		}
		start = found + 1
	}
	return first, count
}

func editLineAtOffset(content []byte, offset int) int {
	if offset < 0 {
		return 1
	}
	return bytes.Count(content[:min(offset, len(content))], []byte{'\n'}) + 1
}
