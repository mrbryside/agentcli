package agentcli

import (
	"context"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
)

func TestBackgroundTaskReminderIsSessionScopedAndEphemeral(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{}, 2)}
	manager := newTestSubagentManager(t, model, 0)
	defer manager.Close()
	for _, scope := range []struct {
		session string
		turn    string
	}{
		{session: "mainAgent-a", turn: "turn-a"},
		{session: "mainAgent-b", turn: "turn-b"},
	} {
		if err := manager.mainAgent.responseScopes.BeginMainAgentTurn(scope.session, scope.turn); err != nil {
			t.Fatal(err)
		}
	}
	first, err := manager.ExecuteTask(context.Background(), taskRequest{
		MainAgentSessionID: "mainAgent-a", MainAgentTurnID: "turn-a",
		AgentName: "researcher", Description: "<label>", Prompt: "<subagent answer>", Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.ExecuteTask(context.Background(), taskRequest{
		MainAgentSessionID: "mainAgent-b", MainAgentTurnID: "turn-b",
		AgentName: "researcher", Description: "other", Prompt: "other", Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := subagentReminderProvider(manager)
	request := agentruntime.ContextReminderRequest{SessionID: "mainAgent-a", TurnID: "turn-a"}
	reminders, err := provider(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 {
		t.Fatalf("reminders = %#v", reminders)
	}
	content := reminders[0].Content
	if !strings.Contains(content, "<active_background_tasks>") ||
		!strings.Contains(content, "<task_id>"+first.TaskID+"</task_id>") ||
		!strings.Contains(content, "<agent>researcher</agent>") ||
		!strings.Contains(content, "<state>running</state>") ||
		strings.Contains(content, second.TaskID) {
		t.Fatalf("session-scoped background reminder = %q", content)
	}
	if strings.Contains(content, "<subagent answer>") || strings.Contains(content, "<label>") {
		t.Fatalf("reminder leaked task content or description: %q", content)
	}

	firstRecord, err := manager.getOwned(context.Background(), "mainAgent-a", first.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	manager.waitForTaskCompletionPublication(context.Background(), first.TaskID, firstRecord.CurrentSubagentTurnID)
	awaitSubagentStatus(t, manager, first.TaskID, "")
	after, err := provider(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("finished background task remained in reminder: %#v", after)
	}

	model.releases <- struct{}{}
}

func TestForegroundAndRetainedTasksAreNotInjected(t *testing.T) {
	model := &scriptedModel{}
	manager := newTestSubagentManager(t, model, 0)
	defer manager.Close()
	result, err := manager.ExecuteTask(context.Background(), taskRequest{
		MainAgentSessionID: "mainAgent", MainAgentTurnID: "turn",
		AgentName: "researcher", Description: "research", Prompt: "work",
	})
	if err != nil || result.State != TaskStateCompleted {
		t.Fatalf("task result = %#v, err = %v", result, err)
	}
	reminders, err := subagentReminderProvider(manager)(context.Background(), agentruntime.ContextReminderRequest{
		SessionID: "mainAgent", TurnID: "turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 0 {
		t.Fatalf("foreground or retained task was injected: %#v", reminders)
	}
}

func TestComposeSubagentReminderProvidersCopiesAndPreservesOrder(t *testing.T) {
	caller := func(context.Context, agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		return []agentruntime.ContextReminder{{Content: "caller"}}, nil
	}
	background := func(context.Context, agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		return []agentruntime.ContextReminder{{Content: "background"}}, nil
	}
	provider := composeContextReminderProviders(caller, background)
	got, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "caller" || got[1].Content != "background" {
		t.Fatalf("reminders = %#v", got)
	}
	got[0].Content = "mutated"
	again, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "turn"})
	if err != nil || again[0].Content != "caller" {
		t.Fatalf("copied reminders = %#v, err = %v", again, err)
	}
}
