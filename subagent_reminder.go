package agentcli

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
)

type subagentReminderKey struct {
	sessionID string
	turnID    string
}

type autoClosedSubagentNotice struct {
	ID          string
	DisplayName string
	Status      storage.SubagentResultStatus
}

func (m *subagentManager) recordAutoClosedSubagent(record storage.Subagent) {
	if m == nil || record.MainAgentSessionID == "" || record.ID == "" {
		return
	}
	notice := autoClosedSubagentNotice{ID: record.ID, DisplayName: record.DisplayName, Status: record.LastResultStatus}
	m.reminderMu.Lock()
	m.pendingAutoClosed[record.MainAgentSessionID] = append(m.pendingAutoClosed[record.MainAgentSessionID], notice)
	m.reminderMu.Unlock()
}

// reserveAutoClosedSubagentReminder assigns pending notices only to a new
// human main-agent turn. The rollback function restores them if turn admission fails.
func (m *subagentManager) reserveAutoClosedSubagentReminder(sessionID, turnID string) func(bool) {
	if m == nil || sessionID == "" || turnID == "" {
		return func(bool) {}
	}
	key := subagentReminderKey{sessionID: sessionID, turnID: turnID}
	m.reminderMu.Lock()
	notices := append([]autoClosedSubagentNotice(nil), m.pendingAutoClosed[sessionID]...)
	if len(notices) != 0 {
		delete(m.pendingAutoClosed, sessionID)
		m.turnAutoClosed[key] = notices
	}
	m.reminderMu.Unlock()

	var once sync.Once
	return func(accepted bool) {
		once.Do(func() {
			if accepted || len(notices) == 0 {
				return
			}
			m.reminderMu.Lock()
			delete(m.turnAutoClosed, key)
			m.pendingAutoClosed[sessionID] = append(notices, m.pendingAutoClosed[sessionID]...)
			m.reminderMu.Unlock()
		})
	}
}

func (m *subagentManager) finishAutoClosedSubagentReminder(sessionID, turnID string) {
	if m == nil {
		return
	}
	m.reminderMu.Lock()
	delete(m.turnAutoClosed, subagentReminderKey{sessionID: sessionID, turnID: turnID})
	m.reminderMu.Unlock()
}

func (m *subagentManager) autoClosedSubagentReminders(sessionID, turnID string) []agentruntime.ContextReminder {
	if m == nil || sessionID == "" || turnID == "" {
		return nil
	}
	m.reminderMu.Lock()
	notices := append([]autoClosedSubagentNotice(nil), m.turnAutoClosed[subagentReminderKey{sessionID: sessionID, turnID: turnID}]...)
	m.reminderMu.Unlock()
	if len(notices) == 0 {
		return nil
	}
	sort.Slice(notices, func(i, j int) bool {
		if notices[i].DisplayName == notices[j].DisplayName {
			return notices[i].ID < notices[j].ID
		}
		return notices[i].DisplayName < notices[j].DisplayName
	})
	var content strings.Builder
	content.WriteString("<subagents_automatically_closed>\n")
	for _, notice := range notices {
		content.WriteString("  <subagent>\n")
		fmt.Fprintf(&content, "    <subagent_id>%s</subagent_id>\n", html.EscapeString(notice.ID))
		fmt.Fprintf(&content, "    <display_name>%s</display_name>\n", html.EscapeString(notice.DisplayName))
		fmt.Fprintf(&content, "    <result_status>%s</result_status>\n", html.EscapeString(string(notice.Status)))
		content.WriteString("  </subagent>\n")
	}
	content.WriteString("  <instruction>These completed or failed subagents were closed automatically after the previous user response. They cannot receive follow-up messages. Start a new subagent only if new work requires one. Closing a subagent is controlled by the host application and is not available as a model action.</instruction>\n")
	content.WriteString("</subagents_automatically_closed>")
	return []agentruntime.ContextReminder{{Content: content.String()}}
}

// subagentReminderProvider derives only session-scoped lifecycle metadata.
// Subagent results arrive automatically; the reminder is the authoritative
// current snapshot for deciding whether to work or wait without polling.
func subagentReminderProvider(manager *subagentManager) agentruntime.ContextReminderProvider {
	const maximumSnapshots = 256
	var snapshotsMu sync.Mutex
	snapshots := make(map[subagentReminderKey][]agentruntime.ContextReminder)
	order := make([]subagentReminderKey, 0, maximumSnapshots)

	return func(ctx context.Context, request agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		if manager == nil || strings.TrimSpace(request.SessionID) == "" {
			return nil, nil
		}
		key := subagentReminderKey{sessionID: request.SessionID, turnID: request.TurnID}
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
		resolved := manager.autoClosedSubagentReminders(request.SessionID, request.TurnID)
		if len(records) == 0 {
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
				snapshotsMu.Unlock()
			}
			return resolved, nil
		}

		var content strings.Builder
		content.WriteString("<active_subagents>\n")
		for _, record := range records {
			unread, err := unreadSubagentMessages(ctx, manager, record.SubagentSessionID, record.ObservedMessageID)
			if err != nil {
				return nil, err
			}
			content.WriteString("  <subagent>\n")
			fmt.Fprintf(&content, "    <subagent_id>%s</subagent_id>\n", html.EscapeString(record.ID))
			fmt.Fprintf(&content, "    <display_name>%s</display_name>\n", html.EscapeString(record.DisplayName))
			fmt.Fprintf(&content, "    <definition_name>%s</definition_name>\n", html.EscapeString(record.DefinitionName))
			fmt.Fprintf(&content, "    <lifecycle_status>%s</lifecycle_status>\n", html.EscapeString(string(record.Status)))
			resultPending := record.Status == storage.SubagentStatusIdle && unread > 0
			if resultPending {
				content.WriteString("    <result_delivery>pending</result_delivery>\n")
			}
			content.WriteString("  </subagent>\n")
		}
		content.WriteString("  <result_policy>A listed subagent may have a pending result. Never poll or inspect it to simulate waiting. Continue only specific independent main-agent work already planned before the assignment. If none remains, stop without assistant content or another tool call. The result arrives automatically as <subagent_result>. Use send_subagent_message only after a delivered incomplete or failed result needs one focused follow-up. Completed and failed subagents close automatically after the user response; incomplete subagents remain available.</result_policy>\n")
		content.WriteString("</active_subagents>")
		resolved = append(resolved, agentruntime.ContextReminder{Content: content.String()})
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

func unreadSubagentMessages(ctx context.Context, manager *subagentManager, subagentSessionID, observedID string) (int, error) {
	messages, err := manager.mainAgent.ListMessages(ctx, subagentSessionID)
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
