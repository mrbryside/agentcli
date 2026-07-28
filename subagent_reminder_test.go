package agentcli

import (
	"context"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

func TestSubagentReminderIsSessionScopedEscapedAndEphemeral(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 3)
	defer manager.Close()
	first, err := manager.Start(context.Background(), "mainAgent-a", "turn", "researcher", "<subagent answer>", "<label>")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start(context.Background(), "mainAgent-b", "turn", "researcher", "other", "")
	if err != nil {
		t.Fatal(err)
	}
	provider := subagentReminderProvider(manager)
	reminders, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent-a", TurnID: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 {
		t.Fatalf("reminders = %#v", reminders)
	}
	content := reminders[0].Content
	if !strings.Contains(content, "<active_subagents>") || !strings.Contains(content, first.ID) || !strings.Contains(content, "<definition_name>researcher</definition_name>") || strings.Contains(content, second.ID) {
		t.Fatalf("session-scoped reminder = %q", content)
	}
	if strings.Contains(content, "<subagent answer>") || strings.Contains(content, "<label>") {
		t.Fatalf("reminder leaked subagent content or label: %q", content)
	}
	for _, hidden := range []string{"<display_name>", "<unread_messages>", "<queued_messages>", "<last_turn_", "<completion_result>", "<result_delivery>"} {
		if strings.Contains(content, hidden) {
			t.Fatalf("reminder exposes obsolete lifecycle field %q: %q", hidden, content)
		}
	}
	messages, err := manager.mainAgent.ListMessages(context.Background(), first.SubagentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "active_subagents") {
			t.Fatalf("persisted reminder in %#v", message)
		}
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, first.ID, storage.SubagentStatusIdle)
	if _, err := manager.CloseSubagent(context.Background(), "mainAgent-a", first.ID); err != nil {
		t.Fatal(err)
	}
	reminders, err = provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent-a", TurnID: "later"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 0 {
		t.Fatalf("closed subagent reminder = %#v", reminders)
	}
}

func TestSubagentReminderIsStableWithinOneMainAgentTurn(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "mainAgent", "subagent-mainAgent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	provider := subagentReminderProvider(manager)
	request := agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "active-mainAgent-turn"}
	before, err := provider(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	after, err := provider(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || len(after) != 1 || before[0].Content != after[0].Content || strings.Contains(after[0].Content, "<result_delivery>") {
		t.Fatalf("same-turn reminder changed after result: before=%#v after=%#v", before, after)
	}
	next, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "next-mainAgent-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || !strings.Contains(next[0].Content, "<lifecycle_status>idle</lifecycle_status>") || strings.Contains(next[0].Content, "<last_turn_") || strings.Contains(next[0].Content, "<result_delivery>") {
		t.Fatalf("next-turn reminder does not expose resumable task state safely: %#v", next)
	}
}

func TestAutoClosedSubagentReminderAppearsOnceOnReservedHumanTurn(t *testing.T) {
	manager := newTestSubagentManager(t, &scriptedModel{}, 1)
	defer manager.Close()
	manager.recordAutoClosedSubagent(storage.Subagent{
		ID: "subagent-closed", DisplayName: "ember-fox", MainAgentSessionID: "mainAgent",
		LastResultStatus: storage.SubagentResultCompleted,
	})
	provider := subagentReminderProvider(manager)
	resultTurn, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "result-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resultTurn) != 0 {
		t.Fatalf("result turn consumed human-only lifecycle reminder: %#v", resultTurn)
	}

	rejectedReservation := manager.reserveAutoClosedSubagentReminder("mainAgent", "rejected-human-turn")
	rejectedReservation(false)
	finishReservation := manager.reserveAutoClosedSubagentReminder("mainAgent", "human-turn")
	finishReservation(true)
	humanTurn, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "human-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(humanTurn) != 1 || !strings.Contains(humanTurn[0].Content, "<subagents_automatically_closed>") || !strings.Contains(humanTurn[0].Content, "ember-fox") || !strings.Contains(humanTurn[0].Content, "controlled by the host application") {
		t.Fatalf("human close reminder = %#v", humanTurn)
	}
	if strings.Contains(humanTurn[0].Content, "close_subagent") {
		t.Fatalf("removed destructive tool appears in lifecycle reminder: %s", humanTurn[0].Content)
	}
	manager.finishAutoClosedSubagentReminder("mainAgent", "human-turn")
	later, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "later-human-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 0 {
		t.Fatalf("auto-close reminder replayed: %#v", later)
	}
}

func TestSubagentReminderShowsOnlyResumableTaskState(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)

	provider := subagentReminderProvider(manager)
	reminders, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "result"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 || !strings.Contains(reminders[0].Content, record.ID) || !strings.Contains(reminders[0].Content, "<lifecycle_status>idle</lifecycle_status>") || strings.Contains(reminders[0].Content, "<last_turn_") || strings.Contains(reminders[0].Content, "<result_delivery>") || !strings.Contains(reminders[0].Content, "Do not inspect, poll, or call a tool merely to wait") {
		t.Fatalf("task reminder = %#v", reminders)
	}
	if _, err := manager.Read(context.Background(), "mainAgent", record.ID, ""); err != nil {
		t.Fatal(err)
	}
	reminders, err = provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 || !strings.Contains(reminders[0].Content, record.ID) || strings.Contains(reminders[0].Content, "<result_delivery>") || strings.Contains(reminders[0].Content, "<last_turn_") {
		t.Fatalf("observed task reminder = %#v", reminders)
	}
}

func TestSubagentReminderShowsFailedTaskAsIdleWithoutPayload(t *testing.T) {
	manager := newTestSubagentManager(t, subagentFailModel{err: context.DeadlineExceeded}, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "mainAgent", "mainAgent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	reminders, err := subagentReminderProvider(manager)(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 || !strings.Contains(reminders[0].Content, record.ID) || !strings.Contains(reminders[0].Content, "<lifecycle_status>idle</lifecycle_status>") || strings.Contains(reminders[0].Content, "<result_delivery>") || strings.Contains(reminders[0].Content, "<last_turn_") {
		t.Fatalf("failed task reminder = %#v", reminders)
	}
}

func TestComposeSubagentReminderProvidersCopiesAndPreservesOrder(t *testing.T) {
	caller := func(context.Context, agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		return []agentruntime.ContextReminder{{Content: "caller"}}, nil
	}
	subagent := func(context.Context, agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		return []agentruntime.ContextReminder{{Content: "subagent"}}, nil
	}
	provider := composeContextReminderProviders(caller, subagent)
	got, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "caller" || got[1].Content != "subagent" {
		t.Fatalf("reminders = %#v", got)
	}
	got[0].Content = "mutated"
	again, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "mainAgent", TurnID: "turn"})
	if err != nil || again[0].Content != "caller" {
		t.Fatalf("copied reminders = %#v, err = %v", again, err)
	}
}

func TestRootAgentComposesCallerAndActiveSubagentReminders(t *testing.T) {
	rootModel := &scriptedModel{}
	messages := inmemory.NewMessageStorage()
	project := &Project{
		config: ProjectConfig{PermissionMode: permission.Default, Providers: map[string]ProviderConfig{
			"test": {Type: ProviderTypeOpenAI, URL: "http://example.invalid", APIKey: "test"},
		}},
		providerName: "test", modelName: "test",
		subagents: map[string]SubagentDefinition{"researcher": {Name: "researcher", Provider: "test", Model: "test", Description: "Research", Instructions: "be useful"}},
	}
	agent, err := New(context.Background(), WithProject(project), WithModel(rootModel), WithMessageStorage(messages), WithContextReminderProvider(func(context.Context, agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		return []agentruntime.ContextReminder{{Content: "caller-reminder"}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	subagentModel := &subagentGateModel{releases: make(chan struct{})}
	agent.subagents.subagentFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(), WithModel(subagentModel), WithMessageStorage(messages))
	}

	first, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "mainAgent", TurnID: "one", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "first"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, first)
	if _, err := agent.StartSubagent(context.Background(), "mainAgent", "one", "researcher", "delegated", ""); err != nil {
		t.Fatal(err)
	}
	second, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "mainAgent", TurnID: "two", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "second"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, second)
	requests := rootModel.Requests()
	if len(requests) != 2 {
		t.Fatalf("root requests = %d", len(requests))
	}
	reminders := requests[1].ContextReminders
	if len(reminders) != 3 ||
		!strings.Contains(reminders[0].Content, "<turn_start>") ||
		reminders[1].Content != "caller-reminder" ||
		!strings.Contains(reminders[2].Content, "<active_subagents>") {
		t.Fatalf("root reminders = %#v", reminders)
	}
}
