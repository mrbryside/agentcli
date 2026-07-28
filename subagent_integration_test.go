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

func TestSubagentIntegrationStepLimitedTaskFinalizesOnceWithoutReportTool(t *testing.T) {
	main := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "limited", Name: TaskToolName,
		Arguments: map[string]any{"agent": "researcher", "description": "Inspect", "prompt": "inspect"},
	}}}
	child := &lateStepLimitFinalTextModel{}
	agent := newIntegrationSubagentAgent(t, main, map[string]*integrationSubagentModel{
		"researcher": newIntegrationSubagentModel("unused"),
	})
	agent.subagents.subagentFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(), withSubagentAgent(), WithModel(child), WithProviderStepLimit(1), WithTool(testTool("work")), WithMessageStorage(agent.messages))
	}

	run, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "main", TurnID: "root", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	requests := child.Requests()
	if len(requests) != 2 || len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "work" || len(requests[1].Tools) != 0 {
		t.Fatalf("child provider requests = %#v, want domain-tool round then one text-only finalizer", requests)
	}
	if len(main.Requests()) != 2 {
		t.Fatalf("main provider requests = %d, want task batch and final response only", len(main.Requests()))
	}
	messages, err := agent.ListMessages(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	result := onlyTaskToolResult(t, messages, "limited")
	if result.State != TaskStateIncomplete || result.Output != "partial final answer" || result.Error != "" {
		t.Fatalf("step-limited task result = %#v", result)
	}
	for _, request := range append(main.Requests(), requests...) {
		for _, tool := range request.Tools {
			if tool.Name == "report_subagent_result" {
				t.Fatal("report_subagent_result was requested during task finalization")
			}
		}
	}
}

func TestSubagentIntegrationBackgroundAndPromotionDeliverOneTrustedResult(t *testing.T) {
	for _, test := range []struct {
		name       string
		background bool
		wait       time.Duration
	}{
		{name: "background", background: true},
		{name: "promotion", wait: time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := map[string]any{"agent": "researcher", "description": "Research", "prompt": "research"}
			if test.background {
				args["background"] = true
			}
			main := &scriptedModel{toolCalls: []provider.ToolCall{{ID: "task", Name: TaskToolName, Arguments: args}}}
			child := newIntegrationSubagentModel("background complete")
			agent := newIntegrationSubagentAgent(t, main, map[string]*integrationSubagentModel{"researcher": child}, WithTaskForegroundWait(test.wait))
			events := agent.SubscribeSystemEvents(context.Background())

			root, err := agent.Start(context.Background(), agentruntime.Request{
				SessionID: "main", TurnID: "root", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate"},
			})
			if err != nil {
				t.Fatal(err)
			}
			child.waitRequests(t, 1)
			waitRun(t, root)
			child.release()
			completed := waitTaskCompleted(t, events)
			if completed.MainAgentTurnID != "root" || completed.TaskCompleted == nil || completed.TaskCompleted.State != TaskStateCompleted {
				t.Fatalf("task completion = %#v", completed)
			}
			waitIntegrationModelRequests(t, main, 3)
			messages, err := agent.ListMessages(context.Background(), "main")
			if err != nil {
				t.Fatal(err)
			}
			trusted := 0
			for _, message := range messages {
				if message.Type == agentruntime.MessageTypeRuntimeEvent && strings.Contains(message.Content, "<task_result>") {
					trusted++
					if strings.Contains(message.Content, "metadata") || strings.Contains(message.Content, "result_progress") {
						t.Fatalf("task input leaked application metadata: %s", message.Content)
					}
				}
			}
			if trusted != 1 {
				t.Fatalf("trusted task-result inputs = %d, want exactly one", trusted)
			}
		})
	}
}

func TestSubagentIntegrationBackgroundFailureDeliversOneTrustedError(t *testing.T) {
	main := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "task", Name: TaskToolName,
		Arguments: map[string]any{"agent": "researcher", "description": "Research", "prompt": "research", "background": true},
	}}}
	agent := newIntegrationSubagentAgent(t, main, map[string]*integrationSubagentModel{"researcher": newIntegrationSubagentModel("unused")})
	agent.subagents.subagentFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(), withSubagentAgent(), WithModel(subagentFailModel{err: errors.New("child unavailable")}), WithMessageStorage(agent.messages))
	}
	events := agent.SubscribeSystemEvents(context.Background())
	run, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "main", TurnID: "root", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	completed := waitTaskCompleted(t, events)
	if completed.TaskCompleted == nil || completed.TaskCompleted.State != TaskStateError {
		t.Fatalf("task completion = %#v, want one error", completed)
	}
	messages, err := agent.ListMessages(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	trusted := 0
	for _, message := range messages {
		if message.Type == agentruntime.MessageTypeRuntimeEvent && strings.Contains(message.Content, "<task_result>") {
			trusted++
			if !strings.Contains(message.Content, `"state":"error"`) {
				t.Fatalf("task error input = %s", message.Content)
			}
		}
	}
	if trusted != 1 {
		t.Fatalf("trusted error task inputs = %d, want exactly one", trusted)
	}
}

func TestSubagentIntegrationResultContractsPublishMetadataOnlyWhenValid(t *testing.T) {
	for _, test := range []struct {
		name      string
		content   string
		wantState TaskState
		metadata  bool
	}{
		{name: "valid", content: `{"message":"Done","requires_reply":true}`, wantState: TaskStateCompleted, metadata: true},
		{name: "invalid", content: `{"requires_reply":true}`, wantState: TaskStateError},
	} {
		t.Run(test.name, func(t *testing.T) {
			main := &scriptedModel{toolCalls: []provider.ToolCall{{
				ID: "contract", Name: TaskToolName,
				Arguments: map[string]any{"agent": "researcher", "description": "Operate", "prompt": "operate"},
			}}}
			child := newIntegrationSubagentModel(test.content)
			child.release()
			agent := newIntegrationSubagentAgent(t, main, map[string]*integrationSubagentModel{"researcher": child})
			definition := agent.subagents.project.subagents["researcher"]
			definition.Result = &AgentResultContract{MessageField: "message", Metadata: map[string]AgentResultMetadataField{
				"requires_reply": {Type: "boolean", Required: true},
			}}
			agent.subagents.project.subagents["researcher"] = definition
			events := agent.SubscribeSystemEvents(context.Background())
			run, err := agent.Start(context.Background(), agentruntime.Request{
				SessionID: "main", TurnID: "root", Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate"},
			})
			if err != nil {
				t.Fatal(err)
			}
			waitRun(t, run)
			completed := waitTaskCompleted(t, events)
			if completed.TaskCompleted == nil || completed.TaskCompleted.State != test.wantState {
				t.Fatalf("task completion = %#v", completed)
			}
			if got := completed.TaskCompleted.Metadata["requires_reply"]; test.metadata && got != true {
				t.Fatalf("task metadata = %#v, want validated requires_reply", completed.TaskCompleted.Metadata)
			} else if !test.metadata && len(completed.TaskCompleted.Metadata) != 0 {
				t.Fatalf("invalid contract published metadata = %#v", completed.TaskCompleted.Metadata)
			}
			messages, err := agent.ListMessages(context.Background(), "main")
			if err != nil {
				t.Fatal(err)
			}
			if result := onlyTaskToolResult(t, messages, "contract"); result.State != test.wantState {
				t.Fatalf("contract task result = %#v", result)
			}
		})
	}
}

func TestSubagentIntegrationHTTPChatCloseHistoryAndReminderRefresh(t *testing.T) {
	mainAgentModel := &scriptedModel{}
	subagentModel := newIntegrationSubagentModel("subagent complete")
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{"researcher": subagentModel})
	server, err := NewServer(agent, WithServerHeartbeat(time.Millisecond))
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
	response = integrationJSONRequest(t, http.MethodPost, httpServer.URL+subagentPath("mainAgent", created.ID)+"/turns", `{"message":"HTTP follow-up"}`)
	if response.StatusCode != http.StatusAccepted {
		defer response.Body.Close()
		t.Fatalf("host-managed HTTP follow-up status = %d", response.StatusCode)
	}
	response.Body.Close()
	subagentModel.waitRequests(t, 2)
	subagentModel.release()
	awaitSubagentStatus(t, agent.subagents, created.ID, storage.SubagentStatusIdle)

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
	if got := integrationResponseUserContents(history.Messages); len(got) != 2 || got[0] != "from HTTP" || got[1] != "HTTP follow-up" {
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

func newIntegrationSubagentAgent(t *testing.T, mainAgent agentruntime.Model, subagentModels map[string]*integrationSubagentModel, options ...Option) *Agent {
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
	agentOptions := []Option{WithProject(project), WithModel(mainAgent), WithMaxSubagents(4)}
	agentOptions = append(agentOptions, options...)
	agent, err := New(context.Background(), agentOptions...)
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

func onlyTaskToolResult(t *testing.T, messages []agentruntime.Message, callID string) TaskResult {
	t.Helper()
	for _, message := range messages {
		if message.ToolResult == nil || message.ToolResult.Name != TaskToolName || message.ToolResult.CallID != callID {
			continue
		}
		var result TaskResult
		if err := json.Unmarshal(message.ToolResult.Output, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Fatalf("missing task result for %q in %#v", callID, messages)
	return TaskResult{}
}

func waitTaskCompleted(t *testing.T, events <-chan SystemEvent) SystemEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != SystemTaskCompleted {
			t.Fatalf("system event = %#v, want task completion", event)
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task completion")
		return SystemEvent{}
	}
}

func waitIntegrationModelRequests(t *testing.T, model *scriptedModel, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(model.Requests()) >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d main provider requests; got %d", count, len(model.Requests()))
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
