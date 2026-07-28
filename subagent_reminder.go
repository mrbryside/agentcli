package agentcli

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
)

// subagentReminderProvider reports only asynchronous work that is still owned
// by the runtime. Foreground and finished task sessions remain resumable by
// task_id but do not need repeated provider context.
func subagentReminderProvider(manager *subagentManager) agentruntime.ContextReminderProvider {
	return func(ctx context.Context, request agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		if manager == nil || strings.TrimSpace(request.SessionID) == "" {
			return nil, nil
		}
		records, err := manager.List(ctx, request.SessionID, false)
		if err != nil {
			return nil, err
		}
		var content strings.Builder
		active := 0
		for _, record := range records {
			if record.ActiveTaskDelivery == nil {
				continue
			}
			if active == 0 {
				content.WriteString("<active_background_tasks>\n")
			}
			state := "result_pending"
			if record.Status == storage.SubagentStatusRunning {
				state = "running"
			}
			content.WriteString("  <task>\n")
			fmt.Fprintf(&content, "    <task_id>%s</task_id>\n", html.EscapeString(record.ID))
			fmt.Fprintf(&content, "    <agent>%s</agent>\n", html.EscapeString(record.DefinitionName))
			fmt.Fprintf(&content, "    <state>%s</state>\n", state)
			content.WriteString("  </task>\n")
			active++
		}
		if active == 0 {
			return nil, nil
		}
		content.WriteString("  <instruction>These background tasks are still being handled. Do not poll them or start duplicate work. Continue only independent work that is already planned.</instruction>\n")
		content.WriteString("</active_background_tasks>")
		return []agentruntime.ContextReminder{{Content: content.String()}}, nil
	}
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
