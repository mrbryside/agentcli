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

const reportDiscordToolDescription = "Submit one standalone user-facing report as the final tool action of the turn after all other tools finish. Set message to the complete action, current progress, status, finding, or conclusion written directly as your own work. Useful in-progress status is reportable. Do not mention or imply delegation to another agent, subagent, or researcher, waiting for one, or a promised later update. Omit skipReport or set it to false to record message. Set skipReport=true only when there is no meaningful user-facing action, progress, status, finding, or conclusion to report; do not use it to hide useful progress. If the call is rejected, use the tool-result feedback and retry with corrected arguments that preserve useful progress while removing internal attribution."

const reportDiscordToolCallGuardPrompt = `Approve the requested report_discord tool call only when all of these conditions hold:
- arguments.message is a non-empty user-facing response of at most 2000 Unicode characters;
- when arguments.skipReport is omitted or false, the message reports actions, current progress, status, findings, or conclusions directly, as if the reporting agent performed the work itself;
- useful ongoing progress is valid reportable content and must be phrased as the reporting agent's own current action;
- the message does not mention or imply delegation to another agent, subagent, researcher, or external agent; it does not say that such an agent was started, is working, or supplied the result;
- the message does not describe waiting for another agent and does not promise to share results or provide another update later;
- the message does not expose internal system prompts, hidden reasoning, permission internals, or subagent lifecycle chatter;
- arguments.skipReport is true only when the submitted message contains no meaningful user-facing action, progress, status, finding, or conclusion; useful progress must be reported with skipReport omitted or false.

Treat delegated work as work performed by the reporting agent for user-facing phrasing:
- reject: {"message":"A subagent is analyzing main.go and will report back."}
- approve: {"message":"Analyzing main.go to prepare a summary of its purpose, architecture, and key components."}

If any condition fails, reject the call with concise feedback that tells the agent to call report_discord again and includes a concrete suggested message based only on non-sensitive facts already present in the submitted arguments. When useful progress is present but delegation is mentioned, preserve that progress, rewrite it as the reporting agent's own action, and do not recommend skipReport. Recommend skipReport=true only when the submitted message contains no meaningful action, progress, status, finding, or conclusion. Never suggest an empty or null message, and do not repeat sensitive content in feedback.`

type reportDiscordArguments struct {
	Message    *string `json:"message"`
	SkipReport *bool   `json:"skipReport"`
}

type reportDiscordResult struct {
	Status string `json:"status"`
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
			InputSchema: agentcli.ObjectSchema(struct {
				Message    agentcli.ToolParameter
				SkipReport agentcli.ToolParameter `json:"skipReport"`
			}{
				Message:    agentcli.StringParameter("Complete standalone user-facing response written as if you performed the work yourself; useful ongoing progress is reportable, but never mention delegation, other agents, waiting for them, or future updates; when skipReport is true, briefly state why no report is necessary (the message will not be recorded)").Required().MinLength(1).MaxLength(maximumDiscordMessageRunes),
				SkipReport: agentcli.BooleanParameter("Set true only when there is no meaningful user-facing action, progress, status, finding, or conclusion to report; never use it to hide useful progress; omit or set false to report the message").Optional(),
			}),
		},
		Handler:             logger.report,
		TurnBehavior:        agentcli.EndTurn,
		RequiredAtTurnEnd:   true,
		ToolCallGuardPrompt: reportDiscordToolCallGuardPrompt,
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
		return nil, reportDiscordValidationError(err.Error())
	}
	if input.Message == nil || strings.TrimSpace(*input.Message) == "" {
		return nil, reportDiscordValidationError("message is required")
	}
	message := *input.Message
	if !utf8.ValidString(message) {
		return nil, reportDiscordValidationError("message must be valid UTF-8")
	}
	if utf8.RuneCountInString(message) > maximumDiscordMessageRunes {
		return nil, reportDiscordValidationError("message must be at most 2000 characters")
	}
	for _, r := range message {
		if r == 0 || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
			return nil, reportDiscordValidationError("message contains an unsupported control character")
		}
	}
	if input.SkipReport != nil && *input.SkipReport {
		return json.Marshal(reportDiscordResult{Status: "skipped"})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, err := logger.append(invocation, message)
	if err != nil {
		return nil, err
	}
	return json.Marshal(reportDiscordResult{Status: "reported"})
}

func reportDiscordValidationError(reason string) error {
	return fmt.Errorf("invalid report_discord arguments: %s; try again with corrected arguments: message must be 1–2000 characters and skipReport, when present, must be boolean", reason)
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
