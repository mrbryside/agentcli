package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mrbryside/agentcli"
)

const (
	reportDiscordDestination   = "discord:#agent-reports"
	maximumDiscordMessageRunes = 2000
)

const reportDiscordToolDescription = "Required final action for every main-agent turn. After all other work and tool results are complete, call this tool exactly once with the complete user-facing response. It deterministically simulates posting to Discord #agent-reports and never performs network I/O. Do not call it early or batch it with another tool."

type reportDiscordArguments struct {
	Message *string `json:"message"`
}

type reportDiscordResult struct {
	Status         string `json:"status"`
	Destination    string `json:"destination"`
	Message        string `json:"message"`
	CharacterCount int    `json:"character_count"`
	NetworkCalled  bool   `json:"network_called"`
}

func newReportDiscordTool() agentcli.Tool {
	return agentcli.Tool{
		Definition: agentcli.ToolDefinition{
			Name:        "report_discord",
			Description: reportDiscordToolDescription,
			InputSchema: agentcli.ObjectSchema(struct{ Message agentcli.ToolParameter }{
				Message: agentcli.StringParameter("Complete user-facing response to simulate sending to Discord as the final action of this turn").Required().MinLength(1).MaxLength(maximumDiscordMessageRunes),
			}),
		},
		Handler:           reportDiscord,
		TurnBehavior:      agentcli.EndTurn,
		RequiredAtTurnEnd: true,
	}
}

func reportDiscord(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
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
	return json.Marshal(reportDiscordResult{
		Status:         "simulated",
		Destination:    reportDiscordDestination,
		Message:        message,
		CharacterCount: utf8.RuneCountInString(message),
		NetworkCalled:  false,
	})
}
