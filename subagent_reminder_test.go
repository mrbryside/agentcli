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
	first, err := manager.Start(context.Background(), "parent-a", "turn", "researcher", "<child answer>", "<label>")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start(context.Background(), "parent-b", "turn", "researcher", "other", "")
	if err != nil {
		t.Fatal(err)
	}
	provider := subagentReminderProvider(manager)
	reminders, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent-a", TurnID: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 {
		t.Fatalf("reminders = %#v", reminders)
	}
	content := reminders[0].Content
	if !strings.Contains(content, "<active_subagents>") || !strings.Contains(content, first.ID) || !strings.Contains(content, "<display_name>"+first.DisplayName+"</display_name>") || strings.Contains(content, second.ID) {
		t.Fatalf("session-scoped reminder = %q", content)
	}
	if strings.Contains(content, "<child answer>") || strings.Contains(content, "<label>") {
		t.Fatalf("reminder leaked child content or label: %q", content)
	}
	if !strings.Contains(content, "<unread_messages>1</unread_messages>") || !strings.Contains(content, "<queued_messages>0</queued_messages>") {
		t.Fatalf("reminder counts = %q", content)
	}
	messages, err := manager.parent.ListMessages(context.Background(), first.SessionID)
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
	observeTestSubagentCallback(t, manager, markTestSubagentCompleted(t, manager, first.ID))
	if _, err := manager.CloseSubagent(context.Background(), "parent-a", first.ID); err != nil {
		t.Fatal(err)
	}
	reminders, err = provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent-a", TurnID: "later"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 0 {
		t.Fatalf("closed child reminder = %#v", reminders)
	}
}

func TestSubagentReminderIsStableWithinOneParentTurn(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "parent", "child-parent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	provider := subagentReminderProvider(manager)
	request := agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "active-parent-turn"}
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
	if len(before) != 1 || len(after) != 1 || before[0].Content != after[0].Content || strings.Contains(after[0].Content, "<completion_callback>") {
		t.Fatalf("same-turn reminder changed after callback: before=%#v after=%#v", before, after)
	}
	next, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "next-parent-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || !strings.Contains(next[0].Content, "<completion_callback>pending</completion_callback>") || strings.Contains(next[0].Content, "<last_turn_outcome>") {
		t.Fatalf("next-turn reminder does not expose callback-pending lifecycle safely: %#v", next)
	}
}

func TestAutoClosedSubagentReminderAppearsOnceOnReservedHumanTurn(t *testing.T) {
	manager := newTestSubagentManager(t, &scriptedModel{}, 1)
	defer manager.Close()
	manager.recordAutoClosedSubagent(storage.Subagent{
		ID: "subagent-closed", DisplayName: "ember-fox", ParentSessionID: "parent",
		LastTurnOutcome: storage.SubagentTurnCompleted,
	})
	provider := subagentReminderProvider(manager)
	callbackTurn, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "callback-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(callbackTurn) != 0 {
		t.Fatalf("callback turn consumed human-only lifecycle reminder: %#v", callbackTurn)
	}

	rejectedReservation := manager.reserveAutoClosedSubagentReminder("parent", "rejected-human-turn")
	rejectedReservation(false)
	finishReservation := manager.reserveAutoClosedSubagentReminder("parent", "human-turn")
	finishReservation(true)
	humanTurn, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "human-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(humanTurn) != 1 || !strings.Contains(humanTurn[0].Content, "<subagents_automatically_closed>") || !strings.Contains(humanTurn[0].Content, "ember-fox") || !strings.Contains(humanTurn[0].Content, "controlled by the host application") {
		t.Fatalf("human close reminder = %#v", humanTurn)
	}
	if strings.Contains(humanTurn[0].Content, "close_subagent") {
		t.Fatalf("removed destructive tool appears in lifecycle reminder: %s", humanTurn[0].Content)
	}
	manager.finishAutoClosedSubagentReminder("parent", "human-turn")
	later, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "later-human-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 0 {
		t.Fatalf("auto-close reminder replayed: %#v", later)
	}
}

func TestSubagentReminderMarksUnreportedUnreadWorkAsIncomplete(t *testing.T) {
	model := &subagentGateModel{releases: make(chan struct{})}
	manager := newTestSubagentManager(t, model, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := model.waitStarts(1); err != nil {
		t.Fatal(err)
	}
	model.releases <- struct{}{}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)

	provider := subagentReminderProvider(manager)
	reminders, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "callback"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 || !strings.Contains(reminders[0].Content, "<completion_callback>pending</completion_callback>") || strings.Contains(reminders[0].Content, "<last_turn_outcome>") || strings.Contains(reminders[0].Content, "<last_turn_summary>") || strings.Contains(reminders[0].Content, "<last_turn_next_step>") || !strings.Contains(reminders[0].Content, "Outcome details are callback-only") {
		t.Fatalf("completion reminder = %#v", reminders)
	}
	if _, err := manager.Read(context.Background(), "parent", record.ID, ""); err != nil {
		t.Fatal(err)
	}
	reminders, err = provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 || strings.Contains(reminders[0].Content, "<completion_callback>") || !strings.Contains(reminders[0].Content, "<last_turn_outcome>incomplete</last_turn_outcome>") {
		t.Fatalf("observed completion reminder = %#v", reminders)
	}
}

func TestSubagentReminderMarksFailedUnreadWorkAsFailed(t *testing.T) {
	manager := newTestSubagentManager(t, subagentFailModel{err: context.DeadlineExceeded}, 1)
	defer manager.Close()
	record, err := manager.Start(context.Background(), "parent", "parent-turn", "researcher", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	awaitSubagentStatus(t, manager, record.ID, storage.SubagentStatusIdle)
	reminders, err := subagentReminderProvider(manager)(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 || !strings.Contains(reminders[0].Content, "<completion_callback>pending</completion_callback>") || strings.Contains(reminders[0].Content, "<last_turn_error>") || strings.Contains(reminders[0].Content, "<last_turn_outcome>") {
		t.Fatalf("failed completion reminder = %#v", reminders)
	}
}

func TestComposeSubagentReminderProvidersCopiesAndPreservesOrder(t *testing.T) {
	caller := func(context.Context, agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		return []agentruntime.ContextReminder{{Content: "caller"}}, nil
	}
	child := func(context.Context, agentruntime.ContextReminderRequest) ([]agentruntime.ContextReminder, error) {
		return []agentruntime.ContextReminder{{Content: "child"}}, nil
	}
	provider := composeContextReminderProviders(caller, child)
	got, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "caller" || got[1].Content != "child" {
		t.Fatalf("reminders = %#v", got)
	}
	got[0].Content = "mutated"
	again, err := provider(context.Background(), agentruntime.ContextReminderRequest{SessionID: "parent", TurnID: "turn"})
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
	childModel := &subagentGateModel{releases: make(chan struct{})}
	agent.subagents.childFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(), WithModel(childModel), WithMessageStorage(messages))
	}

	first, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "parent", TurnID: "one", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "first"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, first)
	if _, err := agent.StartSubagent(context.Background(), "parent", "one", "researcher", "delegated", ""); err != nil {
		t.Fatal(err)
	}
	second, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "parent", TurnID: "two", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "second"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, second)
	requests := rootModel.Requests()
	if len(requests) != 2 {
		t.Fatalf("root requests = %d", len(requests))
	}
	reminders := requests[1].ContextReminders
	if len(reminders) != 2 || reminders[0].Content != "caller-reminder" || !strings.Contains(reminders[1].Content, "<active_subagents>") {
		t.Fatalf("root reminders = %#v", reminders)
	}
}
