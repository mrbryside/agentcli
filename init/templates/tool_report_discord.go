package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/mrbryside/agentcli"
)

const (
	reportDiscordDestination   = "discord:#agent-reports"
	maximumDiscordMessageRunes = 2000
)

const reportDiscordToolDescription = "Required final action for every main-agent turn. After all other work and tool results are complete, call this tool exactly once with the complete user-facing response. It deterministically simulates posting to Discord #agent-reports, appends the payload to report/{session}.json, and never performs network I/O. Do not call it early or batch it with another tool."

type reportDiscordArguments struct {
	Message *string `json:"message"`
}

type reportDiscordResult struct {
	Status         string `json:"status"`
	Destination    string `json:"destination"`
	Message        string `json:"message"`
	CharacterCount int    `json:"character_count"`
	NetworkCalled  bool   `json:"network_called"`
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	CallID         string `json:"call_id"`
	LogPath        string `json:"log_path"`
}

type reportDiscordLogEntry struct {
	Sequence       int    `json:"sequence"`
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	CallID         string `json:"call_id"`
	ToolName       string `json:"tool_name"`
	Status         string `json:"status"`
	Destination    string `json:"destination"`
	Message        string `json:"message"`
	CharacterCount int    `json:"character_count"`
	NetworkCalled  bool   `json:"network_called"`
}

type reportDiscordLogger struct {
	root string
	mu   sync.Mutex
}

func newReportDiscordTool(root string) agentcli.Tool {
	logger := &reportDiscordLogger{root: root}
	return agentcli.Tool{
		Definition: agentcli.ToolDefinition{
			Name:        "report_discord",
			Description: reportDiscordToolDescription,
			InputSchema: agentcli.ObjectSchema(struct{ Message agentcli.ToolParameter }{
				Message: agentcli.StringParameter("Complete user-facing response to simulate sending to Discord as the final action of this turn").Required().MinLength(1).MaxLength(maximumDiscordMessageRunes),
			}),
		},
		Handler:           logger.report,
		TurnBehavior:      agentcli.EndTurn,
		RequiredAtTurnEnd: true,
	}
}

func (logger *reportDiscordLogger) report(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	invocation, ok := agentcli.ToolInvocationFromContext(ctx)
	if !ok {
		return nil, errors.New("report invocation context is required")
	}
	var input reportDiscordArguments
	if err := agentcli.DecodeArguments(raw, &input); err != nil {
		return nil, err
	}
	if input.Message == nil || strings.TrimSpace(*input.Message) == "" {
		return nil, errors.New("report message is required")
	}
	message := *input.Message
	if !utf8.ValidString(message) {
		return nil, errors.New("report message must be valid UTF-8")
	}
	if utf8.RuneCountInString(message) > maximumDiscordMessageRunes {
		return nil, errors.New("report message must be at most 2000 characters")
	}
	for _, r := range message {
		if r == 0 || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
			return nil, errors.New("report message contains an unsupported control character")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logPath, err := logger.append(invocation, message)
	if err != nil {
		return nil, err
	}
	return json.Marshal(reportDiscordResult{
		Status:         "simulated",
		Destination:    reportDiscordDestination,
		Message:        message,
		CharacterCount: utf8.RuneCountInString(message),
		NetworkCalled:  false,
		SessionID:      invocation.SessionID,
		TurnID:         invocation.TurnID,
		CallID:         invocation.CallID,
		LogPath:        logPath,
	})
}

func (logger *reportDiscordLogger) append(invocation agentcli.ToolInvocation, message string) (string, error) {
	if strings.TrimSpace(logger.root) == "" {
		return "", errors.New("report project root is required")
	}
	filename := reportDiscordSessionFilename(invocation.SessionID)
	reportDirectory := filepath.Join(logger.root, "report")
	path := filepath.Join(reportDirectory, filename)
	relativePath := filepath.ToSlash(filepath.Join("report", filename))

	logger.mu.Lock()
	defer logger.mu.Unlock()

	entries := make([]reportDiscordLogEntry, 0, 1)
	if encoded, err := os.ReadFile(path); err == nil {
		if len(encoded) > 0 {
			if err := json.Unmarshal(encoded, &entries); err != nil {
				return "", fmt.Errorf("decode report log: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read report log: %w", err)
	}

	entry := reportDiscordLogEntry{
		Sequence:       len(entries) + 1,
		SessionID:      invocation.SessionID,
		TurnID:         invocation.TurnID,
		CallID:         invocation.CallID,
		ToolName:       invocation.ToolName,
		Status:         "simulated",
		Destination:    reportDiscordDestination,
		Message:        message,
		CharacterCount: utf8.RuneCountInString(message),
		NetworkCalled:  false,
	}
	entries = append(entries, entry)
	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode report log: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(reportDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create report directory: %w", err)
	}
	temporary, err := os.CreateTemp(reportDirectory, ".report-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create report log temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("secure report log temporary file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("write report log: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close report log: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", fmt.Errorf("commit report log: %w", err)
	}
	return relativePath, nil
}

func reportDiscordSessionFilename(sessionID string) string {
	if sessionID == "" {
		return "unknown-session.json"
	}
	var builder strings.Builder
	for _, character := range sessionID {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			builder.WriteRune(character)
			continue
		}
		builder.WriteString(url.PathEscape(string(character)))
	}
	if builder.Len() == 0 || builder.String() == "." || builder.String() == ".." {
		return "unknown-session.json"
	}
	return builder.String() + ".json"
}
