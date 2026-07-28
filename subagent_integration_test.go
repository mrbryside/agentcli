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
)

func TestSubagentIntegrationForegroundTasksRunInParallelAndReturnInMainTurn(t *testing.T) {
	mainAgentModel := &scriptedModel{toolCalls: []provider.ToolCall{
		{ID: "research", Name: TaskToolName, Arguments: map[string]any{"agent": "researcher", "description": "Research first", "prompt": "research first"}},
		{ID: "review", Name: TaskToolName, Arguments: map[string]any{"agent": "reviewer", "description": "Review first", "prompt": "review first"}},
	}}
	researchModel := newIntegrationSubagentModel("research complete")
	reviewModel := newIntegrationSubagentModel("review complete")
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{
		"researcher": researchModel,
		"reviewer":   reviewModel,
	})

	root, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent", TurnID: "mainAgent-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate both"},
	})
	if err != nil {
		t.Fatal(err)
	}
	researchModel.waitRequests(t, 1)
	reviewModel.waitRequests(t, 1)
	if root.Done() {
		t.Fatal("foreground task turn finished before both child results")
	}
	researchModel.release()
	reviewModel.release()
	waitRun(t, root)
	if _, err := root.Result(); err != nil {
		t.Fatal(err)
	}

	requests := mainAgentModel.Requests()
	if len(requests) != 2 {
		t.Fatalf("main provider requests = %d, want task batch then final response", len(requests))
	}
	if len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != TaskToolName {
		t.Fatalf("main catalog = %#v, want only task", requests[0].Tools)
	}
	messages, err := agent.ListMessages(context.Background(), "mainAgent")
	if err != nil {
		t.Fatal(err)
	}
	results := make(map[string]TaskResult)
	for _, message := range messages {
		if message.ToolResult == nil || message.ToolResult.Name != TaskToolName {
			continue
		}
		var result TaskResult
		if err := json.Unmarshal(message.ToolResult.Output, &result); err != nil {
			t.Fatal(err)
		}
		results[message.ToolResult.CallID] = result
	}
	if len(results) != 2 {
		t.Fatalf("task results = %#v, want one result per task call", results)
	}
	for callID, output := range map[string]string{"research": "research complete", "review": "review complete"} {
		result := results[callID]
		if result.TaskID == "" || result.State != TaskStateCompleted || result.Output != output || result.Error != "" {
			t.Fatalf("task %s result = %#v", callID, result)
		}
	}
	if got := mainAgentModel.Requests(); len(got) != 2 {
		t.Fatalf("task result created an unexpected main continuation: %d requests", len(got))
	}
}

// TODO(task-10): add background and foreground-promotion integration coverage
// once Task 6 owns background delivery and removes the legacy result pump.

func TestSubagentIntegrationHTTPChatCloseHistoryAndReminderRefresh(t *testing.T) {
	mainAgentModel := &scriptedModel{}
	subagentModel := newIntegrationSubagentModel("subagent complete")
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{"researcher": subagentModel})
	server, err := NewServer(agent, WithServerHeartbeat(time.Millisecond), WithServerAutoContinueSubagents(false))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = server.Shutdown(context.Background())
	})

	first, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "mainAgent", TurnID: "one", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, first)
	if got := mainAgentModel.Requests()[0].ContextReminders; len(got) != 1 || !strings.Contains(got[0].Content, "<turn_start>") {
		t.Fatalf("initial new-turn reminder = %#v", got)
	}

	response := integrationJSONRequest(t, http.MethodPost, httpServer.URL+"/v1/sessions/mainAgent/subagents", `{"name":"researcher","message":"from HTTP"}`)
	if response.StatusCode != http.StatusCreated {
		defer response.Body.Close()
		t.Fatalf("create HTTP subagent status = %d", response.StatusCode)
	}
	var created SubagentResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	subagentModel.waitRequests(t, 1)

	second, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "mainAgent", TurnID: "two", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, second)
	reminders := mainAgentModel.Requests()[1].ContextReminders
	if len(reminders) != 2 || !strings.Contains(reminders[0].Content, "<turn_start>") || !strings.Contains(reminders[1].Content, created.ID) || !strings.Contains(reminders[1].Content, "<active_subagents>") {
		t.Fatalf("active subagent reminder = %#v", reminders)
	}

	subagentModel.release()
	awaitSubagentStatus(t, agent.subagents, created.ID, storage.SubagentStatusIdle)
	pendingResponse := integrationJSONRequest(t, http.MethodPost, httpServer.URL+subagentPath("mainAgent", created.ID)+"/turns", `{"message":"too early"}`)
	if pendingResponse.StatusCode != http.StatusConflict {
		defer pendingResponse.Body.Close()
		t.Fatalf("send before result consumption status = %d", pendingResponse.StatusCode)
	}
	pendingResponse.Body.Close()
	if _, err := agent.ReadSubagent(context.Background(), "mainAgent", created.ID, ""); err != nil {
		t.Fatal(err)
	}
	response = integrationJSONRequest(t, http.MethodPost, httpServer.URL+subagentPath("mainAgent", created.ID)+"/turns", `{"message":"HTTP follow-up"}`)
	if response.StatusCode != http.StatusConflict {
		defer response.Body.Close()
		t.Fatalf("completed HTTP subagent send status = %d", response.StatusCode)
	}
	response.Body.Close()

	response = integrationJSONRequest(t, http.MethodDelete, httpServer.URL+subagentPath("mainAgent", created.ID), "")
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		t.Fatalf("close HTTP subagent status = %d", response.StatusCode)
	}
	response.Body.Close()
	response = integrationJSONRequest(t, http.MethodGet, httpServer.URL+subagentPath("mainAgent", created.ID)+"/messages", "")
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
	if got := integrationResponseUserContents(history.Messages); len(got) != 1 || got[0] != "from HTTP" {
		t.Fatalf("HTTP subagent history = %#v", history)
	}

	third, err := agent.Start(context.Background(), agentruntime.Request{SessionID: "mainAgent", TurnID: "three", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "three"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, third)
	if got := mainAgentModel.Requests()[2].ContextReminders; len(got) != 1 || !strings.Contains(got[0].Content, "<turn_start>") || strings.Contains(got[0].Content, "<active_subagents>") {
		t.Fatalf("closed subagent remained in reminder = %#v", got)
	}
	mainAgentMessages, err := agent.ListMessages(context.Background(), "mainAgent")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(integrationMessageContents(mainAgentMessages), "active_subagents") {
		t.Fatalf("ephemeral reminder persisted in mainAgent transcript: %#v", mainAgentMessages)
	}
}

func newIntegrationSubagentAgent(t *testing.T, mainAgent agentruntime.Model, subagentModels map[string]*integrationSubagentModel) *Agent {
	t.Helper()
	definitions := make(map[string]SubagentDefinition, len(subagentModels))
	for name := range subagentModels {
		definitions[name] = SubagentDefinition{Name: name, Description: name + " work", Provider: "test", Model: name + "-model", Instructions: "Return a concise result."}
	}
	project := &Project{
		config: ProjectConfig{PermissionMode: permission.Default, Providers: map[string]ProviderConfig{
			"test": {Type: ProviderTypeOpenAI, URL: "http://example.invalid", APIKey: "test"},
		}},
		providerName: "test", modelName: "mainAgent-model", subagents: definitions,
	}
	agent, err := New(context.Background(), WithProject(project), WithModel(mainAgent), WithMaxSubagents(4))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	agent.subagents.subagentFactory = func(definition SubagentDefinition) (*Agent, error) {
		model := subagentModels[definition.Name]
		if model == nil {
			return nil, errors.New("missing test subagent model")
		}
		return New(context.Background(), withSubagentAgent(), WithModel(model), WithMessageStorage(agent.messages))
	}
	return agent
}

type integrationSubagentModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
	releaseC chan struct{}
	content  string
}

func newIntegrationSubagentModel(content string) *integrationSubagentModel {
	return &integrationSubagentModel{releaseC: make(chan struct{}, 8), content: content}
}

func (m *integrationSubagentModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	return integrationSubagentStream{releaseC: m.releaseC, content: m.content}, nil
}

func (m *integrationSubagentModel) waitRequests(t *testing.T, count int) {
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
	t.Fatal("timed out waiting for subagent provider start")
}

func (m *integrationSubagentModel) release() { m.releaseC <- struct{}{} }

type integrationSubagentStream struct {
	releaseC <-chan struct{}
	content  string
}

func (s integrationSubagentStream) Subscribe(ctx context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	go func() {
		defer close(events)
		select {
		case <-s.releaseC:
			events <- provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: s.content, Finished: true}}}
		case <-ctx.Done():
		}
	}()
	return events
}

func (integrationSubagentStream) Result() (provider.StreamResult, error) {
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
