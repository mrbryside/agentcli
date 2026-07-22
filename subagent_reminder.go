package agentcli

import (
	"context"
	"fmt"
	"html"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
)

// subagentReminderProvider derives only session-scoped lifecycle metadata.
// Child results arrive through callbacks; the reminder is the authoritative
// current snapshot for deciding whether to work or wait without polling.
func subagentReminderProvider(manager *subagentManager) agentruntime.ContextReminderProvider {
	type reminderKey struct {
		sessionID string
		turnID    string
	}
	const maximumSnapshots = 256
	var snapshotsMu sync.Mutex
	snapshots := make(map[reminderKey][]agentruntime.ContextReminder)
	order := make([]reminderKey, 0, maximumSnapshots)

	return func(ctx context.Context, request agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		if manager == nil || strings.TrimSpace(request.SessionID) == "" {
			return nil, nil
		}
		key := reminderKey{sessionID: request.SessionID, turnID: request.TurnID}
		if request.TurnID != "" {
			snapshotsMu.Lock()
			cached, found := snapshots[key]
			snapshotsMu.Unlock()
			if found {
				return cloneSubagentReminders(cached), nil
			}
		}
		records, err := manager.List(ctx, request.SessionID, false)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			if request.TurnID != "" {
				snapshotsMu.Lock()
				if _, found := snapshots[key]; !found {
					snapshots[key] = nil
					order = append(order, key)
					if len(order) > maximumSnapshots {
						delete(snapshots, order[0])
						order = order[1:]
					}
				}
				snapshotsMu.Unlock()
			}
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
			if record.LastTurnOutcome != "" {
				fmt.Fprintf(&content, "    <last_turn_outcome>%s</last_turn_outcome>\n", html.EscapeString(string(record.LastTurnOutcome)))
			}
			if record.LastTurnSummary != "" {
				fmt.Fprintf(&content, "    <last_turn_summary>%s</last_turn_summary>\n", html.EscapeString(record.LastTurnSummary))
			}
			if record.LastTurnNextStep != "" {
				fmt.Fprintf(&content, "    <last_turn_next_step>%s</last_turn_next_step>\n", html.EscapeString(record.LastTurnNextStep))
			}
			fmt.Fprintf(&content, "    <unread_messages>%d</unread_messages>\n", unread)
			fmt.Fprintf(&content, "    <queued_messages>%d</queued_messages>\n", len(record.Pending))
			if record.Status == storage.SubagentStatusIdle && unread > 0 {
				callbackStatus := string(record.LastTurnOutcome)
				if callbackStatus == "" {
					callbackStatus = "incomplete"
				}
				fmt.Fprintf(&content, "    <completion_callback>%s</completion_callback>\n", callbackStatus)
			}
			content.WriteString("  </subagent>\n")
		}
		content.WriteString("  <callback_policy>Dispatch is not completion. Never poll list_subagents or subagent_status while waiting, and never close a running child. Use a delivered callback, do independent work, send one focused follow-up for an incomplete outcome, or end the turn and wait passively for the next callback.</callback_policy>\n")
		content.WriteString("</active_subagents>")
		resolved := []agentruntime.ContextReminder{{Content: content.String()}}
		if request.TurnID != "" {
			snapshotsMu.Lock()
			if _, found := snapshots[key]; !found {
				snapshots[key] = cloneSubagentReminders(resolved)
				order = append(order, key)
				if len(order) > maximumSnapshots {
					delete(snapshots, order[0])
					order = order[1:]
				}
			}
			resolved = cloneSubagentReminders(snapshots[key])
			snapshotsMu.Unlock()
		}
		return resolved, nil
	}
}

func cloneSubagentReminders(reminders []agentruntime.ContextReminder) []agentruntime.ContextReminder {
	if reminders == nil {
		return nil
	}
	cloned := make([]agentruntime.ContextReminder, len(reminders))
	copy(cloned, reminders)
	return cloned
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
