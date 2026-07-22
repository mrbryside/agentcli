package agentcli

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
)

// subagentReminderProvider derives only session-scoped lifecycle metadata.
// Child results arrive through callbacks; the reminder is the authoritative
// current snapshot for deciding whether to work or wait without polling.
func subagentReminderProvider(manager *subagentManager) agentruntime.ContextReminderProvider {
	return func(ctx context.Context, request agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		if manager == nil || strings.TrimSpace(request.SessionID) == "" {
			return nil, nil
		}
		records, err := manager.List(ctx, request.SessionID, false)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return nil, nil
		}

		var content strings.Builder
		content.WriteString("<active_subagents>\n")
		for _, record := range records {
			unread, err := unreadSubagentMessages(ctx, manager, record.SessionID, record.ObservedMessageID)
			if err != nil {
				return nil, err
			}
			content.WriteString("  <subagent>\n")
			fmt.Fprintf(&content, "    <id>%s</id>\n", html.EscapeString(record.ID))
			fmt.Fprintf(&content, "    <display_name>%s</display_name>\n", html.EscapeString(record.DisplayName))
			fmt.Fprintf(&content, "    <definition_name>%s</definition_name>\n", html.EscapeString(record.DefinitionName))
			fmt.Fprintf(&content, "    <status>%s</status>\n", html.EscapeString(string(record.Status)))
			if record.CurrentTurnID != "" {
				fmt.Fprintf(&content, "    <current_turn>%s</current_turn>\n", html.EscapeString(record.CurrentTurnID))
			}
			if record.LastTurnID != "" {
				fmt.Fprintf(&content, "    <last_turn>%s</last_turn>\n", html.EscapeString(record.LastTurnID))
			}
			if record.LastTurnError != "" {
				fmt.Fprintf(&content, "    <last_turn_error>%s</last_turn_error>\n", html.EscapeString(record.LastTurnError))
			}
			fmt.Fprintf(&content, "    <unread_messages>%d</unread_messages>\n", unread)
			fmt.Fprintf(&content, "    <queued_messages>%d</queued_messages>\n", len(record.Pending))
			if record.Status == storage.SubagentStatusIdle && unread > 0 {
				callbackStatus := "ready"
				if record.LastTurnError != "" {
					callbackStatus = "failed"
				}
				fmt.Fprintf(&content, "    <completion_callback>%s</completion_callback>\n", callbackStatus)
			}
			content.WriteString("  </subagent>\n")
		}
		content.WriteString("  <callback_policy>Never poll list_subagents or subagent_status while waiting. Use a delivered callback, do useful work, send a focused follow-up, or end the turn and wait passively for the next callback.</callback_policy>\n")
		content.WriteString("</active_subagents>")
		return []agentruntime.ContextReminder{{Content: content.String()}}, nil
	}
}

func unreadSubagentMessages(ctx context.Context, manager *subagentManager, childSessionID, observedID string) (int, error) {
	messages, err := manager.parent.ListMessages(ctx, childSessionID)
	if err != nil {
		return 0, err
	}
	if observedID == "" {
		return len(messages), nil
	}
	for index, message := range messages {
		if message.ID == observedID {
			return len(messages) - index - 1, nil
		}
	}
	// An observation cursor refers to a message that has since become
	// unavailable only with a non-conforming storage implementation. Counting
	// all retained messages is conservative and never leaks their contents.
	return len(messages), nil
}

func composeContextReminderProviders(providers ...agentruntime.ContextReminderProvider) agentruntime.ContextReminderProvider {
	active := make([]agentruntime.ContextReminderProvider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil {
			active = append(active, provider)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(ctx context.Context, request agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		var reminders []agentruntime.ContextReminder
		for _, provider := range active {
			resolved, err := provider(ctx, request)
			if err != nil {
				return nil, err
			}
			for _, reminder := range resolved {
				reminders = append(reminders, agentruntime.ContextReminder{Content: reminder.Content})
			}
		}
		return reminders, nil
	}
}
