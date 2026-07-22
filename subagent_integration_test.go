package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestSubagentIntegrationParentToolsRunParallelChildrenAndMailbox(t *testing.T) {
	parentModel := &scriptedModel{toolCalls: []provider.ToolCall{
		{ID: "research", Name: StartSubagentToolName, Arguments: map[string]any{"name": "researcher", "message": "research first", "new_instance": true}},
		{ID: "review", Name: StartSubagentToolName, Arguments: map[string]any{"name": "reviewer", "message": "review first", "new_instance": true}},
	}}
	researchModel := newIntegrationChildModel("research complete")
	reviewModel := newIntegrationChildModel("review complete")
	agent := newIntegrationSubagentAgent(t, parentModel, map[string]*integrationChildModel{
		"researcher": researchModel,
		"reviewer":   reviewModel,
	})

	parentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "parent", TurnID: "parent-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate both"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, parentRun)
	for _, request := range parentModel.Requests() {
		if len(request.Tools) != 6 {
			t.Fatalf("parent provider tool count = %d, want static six: %#v", len(request.Tools), request.Tools)
		}
		for _, tool := range request.Tools {
			if tool.Name == "read_subagent" || tool.Name == "wait_subagent" {
				t.Fatalf("parent provider received removed model tool %q", tool.Name)
			}
		}
	}
	researchModel.waitRequests(t, 1)
	reviewModel.waitRequests(t, 1)

	children, err := agent.ListSubagents(context.Background(), "parent", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %#v, want two immediate handles", children)
	}
	byDefinition := make(map[string]storage.Subagent, len(children))
	for _, child := range children {
		if child.Status != storage.SubagentStatusRunning || child.CurrentTurnID == "" {
			t.Fatalf("child was not returned while running: %#v", child)
		}
		byDefinition[child.DefinitionName] = child
	}
	research, review := byDefinition["researcher"], byDefinition["reviewer"]
	if research.ID == "" || review.ID == "" || research.SessionID == review.SessionID {
		t.Fatalf("isolated child handles = research %#v review %#v", research, review)
	}

	changed, err := agent.WaitSubagent(context.Background(), "parent", []string{research.ID}, map[string]uint64{research.ID: research.Version})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].ID != research.ID {
		t.Fatalf("wait changed = %#v", changed)
	}
	read, err := agent.ReadSubagent(context.Background(), "parent", research.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if read.FinalAnswer != nil || read.NextMessageID == "" {
		t.Fatalf("research read = %#v", read)
	}

	queued, err := agent.SendSubagentMessage(context.Background(), "parent", review.ID, "review follow-up")
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Pending) != 1 || queued.Pending[0].Content != "review follow-up" {
		t.Fatalf("queued review follow-up = %#v", queued)
	}

	researchModel.release()
	awaitSubagentStatus(t, agent.subagents, research.ID, storage.SubagentStatusIdle)
	reviewModel.release()
	reviewModel.waitRequests(t, 2)
	reviewModel.release()
	awaitSubagentStatus(t, agent.subagents, review.ID, storage.SubagentStatusIdle)

	reviewHistory, err := agent.ListMessages(context.Background(), review.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := integrationUserContents(reviewHistory); len(got) != 2 || got[0] != "review first" || got[1] != "review follow-up" {
		t.Fatalf("ordered review history = %#v", reviewHistory)
	}
	researchHistory, err := agent.ListMessages(context.Background(), research.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(integrationMessageContents(researchHistory), "review") {
		t.Fatalf("research history leaked review work: %#v", researchHistory)
	}
}

func TestSubagentIntegrationFinishTurnAllowsSequentialDispatch(t *testing.T) {
	parentModel := &integrationSequentialDispatchParentModel{}
	agent := newIntegrationSubagentAgent(t, parentModel, map[string]*integrationChildModel{
		"researcher": newIntegrationChildModel("research complete"),
		"reviewer":   newIntegrationChildModel("review complete"),
	})

	parentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "parent", TurnID: "parent-turn-sequential",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate sequentially"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, parentRun)
	if got := parentModel.requestCount(); got != 2 {
		t.Fatalf("parent provider requests = %d, want two dispatch rounds", got)
	}
	children, err := agent.ListSubagents(context.Background(), "parent", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %#v, want two sequential dispatches", children)
	}
}

func TestSubagentIntegrationFastCallbackDoesNotTriggerSpeculativeParentAnswer(t *testing.T) {
	parentModel := newIntegrationPendingCallbackParentModel()
	childModel := newIntegrationChildModel("Who should receive the transfer?")
	agent := newIntegrationSubagentAgent(t, parentModel, map[string]*integrationChildModel{
		"researcher": childModel,
	})

	parentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "parent", TurnID: "parent-fast-callback",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate then follow up"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parentModel.waitForSecondRound(t)
	childModel.waitRequests(t, 1)
	childModel.release()
	children, err := agent.ListSubagents(context.Background(), "parent", false)
	if err != nil || len(children) != 1 {
		t.Fatalf("children before callback = %#v, err = %v", children, err)
	}
	awaitSubagentStatus(t, agent.subagents, children[0].ID, storage.SubagentStatusIdle)
	parentModel.releaseSecondRound()
	waitRun(t, parentRun)

	if got := parentModel.requestCount(); got != 2 {
		t.Fatalf("parent provider requests = %d, want 2 without speculative recovery round", got)
	}
	messages, err := agent.ListMessages(context.Background(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	foundAlreadySent := false
	for _, message := range messages {
		if message.ToolResult == nil || message.ToolResult.Name != SendSubagentMessageToolName {
			continue
		}
		var result struct {
			Action toolexecution.SubagentSendAction `json:"action"`
		}
		if err := json.Unmarshal(message.ToolResult.Output, &result); err != nil {
			t.Fatal(err)
		}
		foundAlreadySent = message.ToolResult.Status == agentruntime.ToolResultSucceeded && result.Action == toolexecution.SubagentSendAlreadySent
	}
	if !foundAlreadySent {
		t.Fatalf("parent transcript has no controlled already_sent result: %#v", messages)
	}
}

func TestSubagentIntegrationCompletionCallbackContinuesParent(t *testing.T) {
	parentModel := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "research", Name: StartSubagentToolName,
		Arguments: map[string]any{"name": "researcher", "message": "inspect the project"},
	}}}
	childModel := newIntegrationCompletedChildModel("compact child finding", "Project inspection is complete.")
	agent := newIntegrationSubagentAgent(t, parentModel, map[string]*integrationChildModel{"researcher": childModel})
	callbacks := agent.SubscribeSubagentCallbacks(context.Background())

	parentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "parent", TurnID: "parent-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate research"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, parentRun)
	childModel.waitRequests(t, 1)
	childModel.release()
	childModel.waitRequests(t, 2)
	childModel.release()

	var callback SubagentCallback
	select {
	case callback = <-callbacks:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for child callback")
	}
	if callback.Status != SubagentCallbackCompleted || callback.Summary != "Project inspection is complete." || callback.FinalAnswer == nil || callback.FinalAnswer.Content != "compact child finding" {
		t.Fatalf("callback = %#v", callback)
	}
	continuation, subscription, err := agent.ContinueSubagentCallbackSubscribed(context.Background(), callback)
	if err != nil {
		t.Fatal(err)
	}
	for range subscription.Events {
	}
	waitRun(t, continuation)

	messages, err := agent.ListMessages(context.Background(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	foundCallback := false
	for _, message := range messages {
		if message.Type == agentruntime.MessageTypeRuntimeEvent && strings.Contains(message.Content, "compact child finding") {
			foundCallback = true
		}
	}
	if !foundCallback {
		t.Fatalf("parent transcript has no runtime callback: %#v", messages)
	}
	record, err := agent.subagents.getOwned(context.Background(), "parent", callback.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if record.ObservedMessageID != callback.NextMessageID {
		t.Fatalf("observed cursor = %q, want %q", record.ObservedMessageID, callback.NextMessageID)
	}
	if record.LastTurnOutcome != storage.SubagentTurnCompleted || record.LastTurnSummary != "Project inspection is complete." || record.LastTurnNextStep != "" {
		t.Fatalf("stored child outcome = %#v", record)
	}
}

func TestSubagentIntegrationExplicitInterruptStopsChildAfterParentTurnEnds(t *testing.T) {
	childModel := newIntegrationChildModel("never completes without release")
	parentModel := &integrationInterruptParentModel{}
	agent := newIntegrationSubagentAgent(t, parentModel, map[string]*integrationChildModel{"researcher": childModel})

	parentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "parent", TurnID: "parent-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate then wait"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, parentRun)
	childModel.waitRequests(t, 1)
	children, err := agent.ListSubagents(context.Background(), "parent", false)
	if err != nil || len(children) != 1 {
		t.Fatalf("child before parent interrupt = %#v, err = %v", children, err)
	}
	childRun, err := agent.SubagentRun(context.Background(), "parent", children[0].ID, children[0].CurrentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.InterruptSubagent(context.Background(), "parent", children[0].ID, "parent cancelled"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, childRun)
	if _, err := childRun.Result(); !errors.Is(err, agentruntime.ErrRunInterrupted) {
		t.Fatalf("child result after parent interrupt = %v, want ErrRunInterrupted", err)
	}
	awaitSubagentStatus(t, agent.subagents, children[0].ID, storage.SubagentStatusIdle)
}

func TestSubagentIntegrationHTTPChatCloseHistoryAndReminderRefresh(t *testing.T) {
	parentModel := &scriptedModel{}
	childModel := newIntegrationChildModel("child complete")
	agent := newIntegrationSubagentAgent(t, parentModel, map[string]*integrationChildModel{"researcher": childModel})
	server, err := NewServer(agent, WithServerHeartbeat(time.Millisecond), WithServerAutoContinueSubagents(false))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = server.Shutdown(context.Background())
	})

	first, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "parent", TurnID: "one", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, first)
	if got := parentModel.Requests()[0].ContextReminders; len(got) != 0 {
		t.Fatalf("unexpected initial reminders = %#v", got)
	}

	response := integrationJSONRequest(t, http.MethodPost, httpServer.URL+"/v1/sessions/parent/subagents", `{"name":"researcher","message":"from HTTP"}`)
	if response.StatusCode != http.StatusCreated {
		defer response.Body.Close()
		t.Fatalf("create HTTP child status = %d", response.StatusCode)
	}
	var created SubagentResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	childModel.waitRequests(t, 1)

	second, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "parent", TurnID: "two", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, second)
	reminders := parentModel.Requests()[1].ContextReminders
	if len(reminders) != 1 || !strings.Contains(reminders[0].Content, created.ID) || !strings.Contains(reminders[0].Content, "<active_subagents>") {
		t.Fatalf("active child reminder = %#v", reminders)
	}

	childModel.release()
	awaitSubagentStatus(t, agent.subagents, created.ID, storage.SubagentStatusIdle)
	pendingResponse := integrationJSONRequest(t, http.MethodPost, httpServer.URL+subagentPath("parent", created.ID)+"/turns", `{"message":"too early"}`)
	if pendingResponse.StatusCode != http.StatusConflict {
		defer pendingResponse.Body.Close()
		t.Fatalf("send before callback consumption status = %d", pendingResponse.StatusCode)
	}
	pendingResponse.Body.Close()
	if _, err := agent.ReadSubagent(context.Background(), "parent", created.ID, ""); err != nil {
		t.Fatal(err)
	}
	response = integrationJSONRequest(t, http.MethodPost, httpServer.URL+subagentPath("parent", created.ID)+"/turns", `{"message":"HTTP follow-up"}`)
	if response.StatusCode != http.StatusAccepted {
		defer response.Body.Close()
		t.Fatalf("send HTTP child status = %d", response.StatusCode)
	}
	response.Body.Close()
	childModel.waitRequests(t, 2)
	childModel.release()
	awaitSubagentStatus(t, agent.subagents, created.ID, storage.SubagentStatusIdle)
	observeTestSubagentCallback(t, agent.subagents, markTestSubagentCompleted(t, agent.subagents, created.ID))

	response = integrationJSONRequest(t, http.MethodDelete, httpServer.URL+subagentPath("parent", created.ID), "")
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		t.Fatalf("close HTTP child status = %d", response.StatusCode)
	}
	response.Body.Close()
	response = integrationJSONRequest(t, http.MethodGet, httpServer.URL+subagentPath("parent", created.ID)+"/messages", "")
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		t.Fatalf("closed history status = %d", response.StatusCode)
	}
	var history SubagentMessagesResponse
	if err := json.NewDecoder(response.Body).Decode(&history); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if got := integrationResponseUserContents(history.Messages); len(got) != 2 || got[0] != "from HTTP" || got[1] != "HTTP follow-up" {
		t.Fatalf("HTTP child history = %#v", history)
	}

	third, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "parent", TurnID: "three", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "three"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, third)
	if got := parentModel.Requests()[2].ContextReminders; len(got) != 0 {
		t.Fatalf("closed child remained in reminder = %#v", got)
	}
	parentMessages, err := agent.ListMessages(context.Background(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(integrationMessageContents(parentMessages), "active_subagents") {
		t.Fatalf("ephemeral reminder persisted in parent transcript: %#v", parentMessages)
	}
}

func newIntegrationSubagentAgent(t *testing.T, parent agentruntime.Model, childModels map[string]*integrationChildModel) *Agent {
	t.Helper()
	definitions := make(map[string]SubagentDefinition, len(childModels))
	for name := range childModels {
		definitions[name] = SubagentDefinition{Name: name, Description: name + " work", Provider: "test", Model: name + "-model", Instructions: "Return a concise result."}
	}
	project := &Project{
		config: ProjectConfig{PermissionMode: permission.Default, Providers: map[string]ProviderConfig{
			"test": {Type: ProviderTypeOpenAI, URL: "http://example.invalid", APIKey: "test"},
		}},
		providerName: "test", modelName: "parent-model",
		subagents: definitions,
	}
	agent, err := New(context.Background(), WithProject(project), WithModel(parent), WithMaxSubagents(4))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	agent.subagents.childFactory = func(definition SubagentDefinition) (*Agent, error) {
		model := childModels[definition.Name]
		if model == nil {
			return nil, errors.New("missing test child model")
		}
		return New(context.Background(), withChildAgent(), WithModel(model), WithMessageStorage(agent.messages))
	}
	return agent
}

type integrationChildModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
	releaseC chan struct{}
	content  string
	outcome  *toolexecution.SubagentOutcome
}

func newIntegrationChildModel(content string) *integrationChildModel {
	return &integrationChildModel{releaseC: make(chan struct{}, 8), content: content}
}

func newIntegrationCompletedChildModel(content, summary string) *integrationChildModel {
	return &integrationChildModel{
		releaseC: make(chan struct{}, 8), content: content,
		outcome: &toolexecution.SubagentOutcome{Status: toolexecution.SubagentOutcomeCompleted, Summary: summary},
	}
}

func (m *integrationChildModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	stream := integrationChildStream{releaseC: m.releaseC, content: m.content}
	if m.outcome != nil && !integrationHasOutcomeResult(request.Messages) {
		arguments := map[string]any{"status": string(m.outcome.Status), "summary": m.outcome.Summary}
		if m.outcome.NextStep != "" {
			arguments["next_step"] = m.outcome.NextStep
		}
		stream.content = ""
		stream.toolCalls = []provider.ToolCall{{ID: "outcome", Name: toolexecution.SubagentOutcomeToolName, Arguments: arguments}}
	}
	return stream, nil
}

func integrationHasOutcomeResult(messages []agentruntime.Message) bool {
	for _, message := range messages {
		if message.ToolResult != nil && message.ToolResult.Name == toolexecution.SubagentOutcomeToolName && message.ToolResult.Status == agentruntime.ToolResultSucceeded {
			return true
		}
	}
	return false
}

func (m *integrationChildModel) waitRequests(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		started := len(m.requests) >= count
		m.mu.Unlock()
		if started {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for child provider start")
}

func (m *integrationChildModel) release() { m.releaseC <- struct{}{} }

type integrationChildStream struct {
	releaseC  <-chan struct{}
	content   string
	toolCalls []provider.ToolCall
}

func (s integrationChildStream) Subscribe(ctx context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	go func() {
		defer close(events)
		select {
		case <-s.releaseC:
			events <- provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: s.content, CompletedTools: s.toolCalls, Finished: true}}}
		case <-ctx.Done():
		}
	}()
	return events
}

func (integrationChildStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, errors.New("unused")
}

type integrationInterruptParentModel struct {
	mu          sync.Mutex
	starts      int
	secondRound chan struct{}
}

type integrationSequentialDispatchParentModel struct {
	mu       sync.Mutex
	requests int
}

type integrationPendingCallbackParentModel struct {
	mu            sync.Mutex
	requests      int
	secondEntered chan struct{}
	secondRelease chan struct{}
}

func newIntegrationPendingCallbackParentModel() *integrationPendingCallbackParentModel {
	return &integrationPendingCallbackParentModel{
		secondEntered: make(chan struct{}),
		secondRelease: make(chan struct{}),
	}
}

func (m *integrationPendingCallbackParentModel) Start(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	m.requests++
	round := m.requests
	m.mu.Unlock()

	if round == 1 {
		call := provider.ToolCall{ID: "start", Name: StartSubagentToolName, Arguments: map[string]any{
			"name": "researcher", "message": "ask for the missing transfer details", "finish_turn": false,
		}}
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{call}, Finished: true}}, nil
	}
	if round == 2 {
		close(m.secondEntered)
		select {
		case <-m.secondRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		childID := integrationStartedSubagentID(request.Messages)
		if childID == "" {
			return nil, errors.New("start_subagent result did not contain a child ID")
		}
		call := provider.ToolCall{ID: "send", Name: SendSubagentMessageToolName, Arguments: map[string]any{
			"subagent_id": childID, "message": "ask again", "finish_turn": true,
		}}
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{call}, Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{Content: "speculative parent question", Finished: true}}, nil
}

func (m *integrationPendingCallbackParentModel) waitForSecondRound(t *testing.T) {
	t.Helper()
	select {
	case <-m.secondEntered:
	case <-time.After(time.Second):
		t.Fatal("parent did not reach second provider round")
	}
}

func (m *integrationPendingCallbackParentModel) releaseSecondRound() {
	close(m.secondRelease)
}

func (m *integrationPendingCallbackParentModel) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

func integrationStartedSubagentID(messages []agentruntime.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		result := messages[index].ToolResult
		if result == nil || result.Name != StartSubagentToolName || result.Status != agentruntime.ToolResultSucceeded {
			continue
		}
		var output struct {
			SubagentID string `json:"subagent_id"`
		}
		if json.Unmarshal(result.Output, &output) == nil {
			return output.SubagentID
		}
	}
	return ""
}

func (m *integrationSequentialDispatchParentModel) Start(_ context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	m.requests++
	round := m.requests
	m.mu.Unlock()

	call := provider.ToolCall{ID: "research", Name: StartSubagentToolName, Arguments: map[string]any{
		"name": "researcher", "message": "research this", "new_instance": true, "finish_turn": false,
	}}
	if round == 2 {
		call = provider.ToolCall{ID: "review", Name: StartSubagentToolName, Arguments: map[string]any{
			"name": "reviewer", "message": "review this", "new_instance": true, "finish_turn": true,
		}}
	}
	return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{call}, Finished: true}}, nil
}

func (m *integrationSequentialDispatchParentModel) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

func (m *integrationInterruptParentModel) Start(_ context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	if m.starts == 1 {
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "delegate", Name: StartSubagentToolName, Arguments: map[string]any{"name": "researcher", "message": "long task"}}}, Finished: true}}, nil
	}
	if m.secondRound == nil {
		m.secondRound = make(chan struct{})
		close(m.secondRound)
	}
	return integrationBlockingStream{}, nil
}

func (m *integrationInterruptParentModel) waitForSecondRound(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		started := m.starts >= 2
		m.mu.Unlock()
		if started {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("parent did not reach second provider round")
}

type integrationBlockingStream struct{}

func (integrationBlockingStream) Subscribe(ctx context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent)
	go func() {
		defer close(events)
		<-ctx.Done()
	}()
	return events
}

func (integrationBlockingStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, errors.New("unused")
}

func integrationJSONRequest(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func integrationUserContents(messages []agentruntime.Message) []string {
	contents := make([]string, 0)
	for _, message := range messages {
		if message.Type == agentruntime.MessageTypeUser {
			contents = append(contents, message.Content)
		}
	}
	return contents
}

func integrationResponseUserContents(messages []MessageResponse) []string {
	contents := make([]string, 0)
	for _, message := range messages {
		if message.Type == agentruntime.MessageTypeUser {
			contents = append(contents, message.Content)
		}
	}
	return contents
}

func integrationMessageContents(messages []agentruntime.Message) string {
	contents := make([]string, len(messages))
	for index, message := range messages {
		contents[index] = message.Content
	}
	return strings.Join(contents, "\n")
}
