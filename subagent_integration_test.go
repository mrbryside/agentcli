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

func TestSubagentIntegrationMainAgentToolsRunParallelSubagentsAndMailbox(t *testing.T) {
	mainAgentModel := &scriptedModel{toolCalls: []provider.ToolCall{
		{ID: "research", Name: StartSubagentToolName, Arguments: map[string]any{"name": "researcher", "message": "research first", "continue_main_agent": true}},
		{ID: "review", Name: StartSubagentToolName, Arguments: map[string]any{"name": "reviewer", "message": "review first", "continue_main_agent": true}},
	}}
	researchModel := newIntegrationSubagentModel("research complete")
	reviewModel := newIntegrationSubagentModel("review complete")
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{
		"researcher": researchModel,
		"reviewer":   reviewModel,
	})

	mainAgentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent", TurnID: "mainAgent-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate both"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, mainAgentRun)
	for _, request := range mainAgentModel.Requests() {
		if len(request.Tools) != 2 {
			t.Fatalf("mainAgent provider tool count = %d, want two routing tools: %#v", len(request.Tools), request.Tools)
		}
		for _, tool := range request.Tools {
			if tool.Name == "read_subagent" || tool.Name == "wait_subagent" || tool.Name == "list_subagents" || tool.Name == "subagent_status" {
				t.Fatalf("mainAgent provider received removed model tool %q", tool.Name)
			}
		}
	}
	researchModel.waitRequests(t, 1)
	reviewModel.waitRequests(t, 1)

	subagents, err := agent.ListSubagents(context.Background(), "mainAgent", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(subagents) != 2 {
		t.Fatalf("subagents = %#v, want two immediate handles", subagents)
	}
	byDefinition := make(map[string]storage.Subagent, len(subagents))
	for _, subagent := range subagents {
		if subagent.Status != storage.SubagentStatusRunning || subagent.CurrentSubagentTurnID == "" {
			t.Fatalf("subagent was not returned while running: %#v", subagent)
		}
		byDefinition[subagent.DefinitionName] = subagent
	}
	research, review := byDefinition["researcher"], byDefinition["reviewer"]
	if research.ID == "" || review.ID == "" || research.SubagentSessionID == review.SubagentSessionID {
		t.Fatalf("isolated subagent handles = research %#v review %#v", research, review)
	}

	changed, err := agent.WaitSubagent(context.Background(), "mainAgent", []string{research.ID}, map[string]uint64{research.ID: research.Version})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].ID != research.ID {
		t.Fatalf("wait changed = %#v", changed)
	}
	read, err := agent.ReadSubagent(context.Background(), "mainAgent", research.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if read.FinalAnswer != nil || read.LastMessageID == "" {
		t.Fatalf("research read = %#v", read)
	}

	queued, err := agent.SendSubagentMessage(context.Background(), "mainAgent", review.ID, "review follow-up")
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

	reviewHistory, err := agent.ListMessages(context.Background(), review.SubagentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := integrationUserContents(reviewHistory); len(got) != 2 || got[0] != "review first" || got[1] != "review follow-up" {
		t.Fatalf("ordered review history = %#v", reviewHistory)
	}
	researchHistory, err := agent.ListMessages(context.Background(), research.SubagentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(integrationMessageContents(researchHistory), "review") {
		t.Fatalf("research history leaked review work: %#v", researchHistory)
	}
}

func TestSubagentIntegrationStartAlwaysAllowsSequentialAssignment(t *testing.T) {
	mainAgentModel := &integrationSequentialAssignmentMainAgentModel{}
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{
		"researcher": newIntegrationSubagentModel("research complete"),
		"reviewer":   newIntegrationSubagentModel("review complete"),
	})

	mainAgentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent", TurnID: "mainAgent-turn-sequential",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate sequentially"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, mainAgentRun)
	if got := mainAgentModel.requestCount(); got != 3 {
		t.Fatalf("mainAgent provider requests = %d, want two assignment rounds and one completion round", got)
	}
	subagents, err := agent.ListSubagents(context.Background(), "mainAgent", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(subagents) != 2 {
		t.Fatalf("subagents = %#v, want two sequential assignments", subagents)
	}
}

func TestSubagentIntegrationFastResultDoesNotTriggerSpeculativeMainAgentAnswer(t *testing.T) {
	mainAgentModel := newIntegrationPendingResultMainAgentModel()
	subagentModel := newIntegrationSubagentModel("Who should receive the transfer?")
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{
		"researcher": subagentModel,
	})

	mainAgentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent", TurnID: "mainAgent-fast-result",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate then follow up"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mainAgentModel.waitForSecondRound(t)
	subagentModel.waitRequests(t, 1)
	subagentModel.release()
	subagents, err := agent.ListSubagents(context.Background(), "mainAgent", false)
	if err != nil || len(subagents) != 1 {
		t.Fatalf("subagents before result = %#v, err = %v", subagents, err)
	}
	awaitSubagentStatus(t, agent.subagents, subagents[0].ID, storage.SubagentStatusIdle)
	mainAgentModel.releaseSecondRound()
	waitRun(t, mainAgentRun)

	if got := mainAgentModel.requestCount(); got != 2 {
		t.Fatalf("mainAgent provider requests = %d, want start and the repeated-send round to end without another provider step", got)
	}
	messages, err := agent.ListMessages(context.Background(), "mainAgent")
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
		t.Fatalf("mainAgent transcript has no controlled already_sent result: %#v", messages)
	}
	if strings.Contains(integrationMessageContents(messages), "speculative mainAgent question") {
		t.Fatalf("mainAgent produced a speculative answer while waiting for result: %#v", messages)
	}
}

func TestSubagentIntegrationCompletionResultContinuesMainAgent(t *testing.T) {
	mainAgentModel := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "research", Name: StartSubagentToolName,
		Arguments: map[string]any{"name": "researcher", "message": "inspect the project", "continue_main_agent": true},
	}}}
	subagentModel := newIntegrationCompletedSubagentModel("compact subagent finding", "Project inspection is complete.")
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{"researcher": subagentModel})
	results := agent.SubscribeSubagentResults(context.Background())

	mainAgentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent", TurnID: "mainAgent-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate research"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, mainAgentRun)
	subagentModel.waitRequests(t, 1)
	subagentModel.release()
	subagentModel.waitRequests(t, 2)
	subagentModel.release()

	var subagentResult SubagentResult
	select {
	case subagentResult = <-results:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subagent result")
	}
	if subagentResult.Status != SubagentResultCompleted || subagentResult.Summary != "Project inspection is complete." || subagentResult.FinalAnswer == nil || subagentResult.FinalAnswer.Content != "compact subagent finding" {
		t.Fatalf("result = %#v", subagentResult)
	}
	continuation, subscription, err := agent.ContinueSubagentResultSubscribed(context.Background(), subagentResult)
	if err != nil {
		t.Fatal(err)
	}
	for range subscription.Events {
	}
	waitRun(t, continuation)

	messages, err := agent.ListMessages(context.Background(), "mainAgent")
	if err != nil {
		t.Fatal(err)
	}
	foundResult := false
	foundProgress := false
	for _, message := range messages {
		if message.Type == agentruntime.MessageTypeRuntimeEvent && strings.Contains(message.Content, "compact subagent finding") {
			foundResult = true
			foundProgress = strings.Contains(message.Content, `"result_progress"`) &&
				strings.Contains(message.Content, `"pending_count":0`) &&
				strings.Contains(message.Content, `"all_results_delivered":true`) &&
				strings.Contains(message.Content, `"delivered_results":[{"subagent_id":"`+subagentResult.SubagentID+`"`)
		}
	}
	if !foundResult {
		t.Fatalf("mainAgent transcript has no runtime result: %#v", messages)
	}
	if !foundProgress {
		t.Fatalf("mainAgent runtime result has no complete result-progress snapshot: %#v", messages)
	}
	record, err := agent.subagents.getOwned(context.Background(), "mainAgent", subagentResult.SubagentID)
	if err != nil {
		t.Fatal(err)
	}
	if record.ObservedMessageID != subagentResult.LastMessageID {
		t.Fatalf("observed cursor = %q, want %q", record.ObservedMessageID, subagentResult.LastMessageID)
	}
	if record.LastResultStatus != storage.SubagentResultCompleted || record.LastResultSummary != "Project inspection is complete." || record.LastResultNextStep != "" {
		t.Fatalf("stored subagent outcome = %#v", record)
	}
}

func TestSubagentIntegrationStartCanEndMainAgentTurnAfterAssignment(t *testing.T) {
	mainAgentModel := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "research", Name: StartSubagentToolName,
		Arguments: map[string]any{
			"name": "researcher", "message": "inspect the project",
			"continue_main_agent": false,
		},
	}}}
	subagentModel := newIntegrationSubagentModel("research complete")
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{"researcher": subagentModel})
	defer agent.Close()

	mainAgentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent-end-after-assignment", TurnID: "mainAgent-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate and wait"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, mainAgentRun)
	if _, err := mainAgentRun.Result(); err != nil {
		t.Fatal(err)
	}
	if requests := mainAgentModel.Requests(); len(requests) != 1 {
		t.Fatalf("mainAgent provider requests = %d, want assignment round only", len(requests))
	}
	subagentModel.waitRequests(t, 1)

	messages, err := agent.ListMessages(context.Background(), "mainAgent-end-after-assignment")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, message := range messages {
		if message.ToolResult == nil || message.ToolResult.Name != StartSubagentToolName {
			continue
		}
		found = strings.Contains(string(message.ToolResult.Output), `"main_agent_action":"stop_and_wait"`) &&
			strings.Contains(string(message.ToolResult.Output), "current turn ends automatically")
	}
	if !found {
		t.Fatalf("start_subagent result has no terminal turn action: %#v", messages)
	}
}

func TestEndResponseScopeWaitsForSubagentResultAndRunsLatestReportOnce(t *testing.T) {
	mainAgentModel := &responseScopeIntegrationMainAgentModel{}
	subagentModel := newIntegrationCompletedSubagentModel("verified finding", "Research completed.")
	delivered := make(chan string, 2)
	report := Tool{
		Definition: ToolDefinition{
			Name:        "report",
			Description: "Deliver the final response.",
			InputSchema: ObjectSchema(struct{ Message ToolParameter }{
				Message: StringParameter("Final message").Required(),
			}),
		},
		Handler: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var input struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, err
			}
			delivered <- input.Message
			return json.RawMessage(`{"sent":true}`), nil
		},
		Trigger:          EndResponseScope,
		EndTurnOnSuccess: true,
	}
	definition := SubagentDefinition{
		Name: "researcher", Description: "research work", Provider: "test",
		Model: "researcher-model", Instructions: "Return a concise result.",
	}
	project := &Project{
		config: ProjectConfig{PermissionMode: permission.Default, Providers: map[string]ProviderConfig{
			"test": {Type: ProviderTypeOpenAI, URL: "http://example.invalid", APIKey: "test"},
		}},
		providerName: "test", modelName: "mainAgent-model",
		subagents: map[string]SubagentDefinition{"researcher": definition},
	}
	agent, err := New(context.Background(), WithProject(project), WithModel(mainAgentModel), WithTool(report), WithMaxSubagents(1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	agent.subagents.subagentFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(), withSubagentAgent(), WithModel(subagentModel), WithMessageStorage(agent.messages))
	}
	results := agent.SubscribeSubagentResults(context.Background())

	root, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "scope-mainAgent", TurnID: "scope-root",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "research and report"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, root)
	if got := mainAgentModel.requestCount(); got != 2 {
		t.Fatalf("mainAgent provider requests before result = %d, want assignment then one yielding report", got)
	}
	select {
	case message := <-delivered:
		t.Fatalf("report handler ran before result: %q", message)
	case <-time.After(20 * time.Millisecond):
	}

	subagentModel.waitRequests(t, 1)
	subagentModel.release()
	subagentModel.waitRequests(t, 2)
	subagentModel.release()
	var result SubagentResult
	select {
	case result = <-results:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subagent result")
	}
	continuation, subscription, err := agent.ContinueSubagentResultSubscribed(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	for range subscription.Events {
	}
	waitRun(t, continuation)

	select {
	case message := <-delivered:
		if message != "final verified response" {
			t.Fatalf("delivered message = %q, want latest result report", message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for after-scope handler")
	}
	messages, err := agent.ListMessages(context.Background(), "scope-mainAgent")
	if err != nil {
		t.Fatal(err)
	}
	assistantCount := 0
	for _, message := range messages {
		if message.Type == agentruntime.MessageTypeAssistant {
			assistantCount++
		}
	}
	if assistantCount != 0 {
		t.Fatalf("assistant messages = %d, want no synthetic response", assistantCount)
	}
	select {
	case duplicate := <-delivered:
		t.Fatalf("handler ran more than once: %q", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestServerAutoResultContinuesOriginatingResponseScope(t *testing.T) {
	mainAgentModel := &responseScopeIntegrationMainAgentModel{}
	subagentModel := newIntegrationCompletedSubagentModel("verified finding", "Research completed.")
	delivered := make(chan string, 2)
	report := Tool{
		Definition: ToolDefinition{
			Name:        "report",
			Description: "Deliver the final response.",
			InputSchema: ObjectSchema(struct{ Message ToolParameter }{
				Message: StringParameter("Final message").Required(),
			}),
		},
		Handler: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var input struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, err
			}
			delivered <- input.Message
			return json.RawMessage(`{"sent":true}`), nil
		},
		Trigger:          EndResponseScope,
		EndTurnOnSuccess: true,
	}
	definition := SubagentDefinition{
		Name: "researcher", Description: "research work", Provider: "test",
		Model: "researcher-model", Instructions: "Return a concise result.",
	}
	project := &Project{
		config: ProjectConfig{PermissionMode: permission.Default, Providers: map[string]ProviderConfig{
			"test": {Type: ProviderTypeOpenAI, URL: "http://example.invalid", APIKey: "test"},
		}},
		providerName: "test", modelName: "mainAgent-model",
		subagents: map[string]SubagentDefinition{"researcher": definition},
	}
	agent, err := New(context.Background(), WithProject(project), WithModel(mainAgentModel), WithTool(report), WithMaxSubagents(1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	agent.subagents.subagentFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(), withSubagentAgent(), WithModel(subagentModel), WithMessageStorage(agent.messages))
	}
	server, err := NewServer(agent, WithServerHeartbeat(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = server.Shutdown(context.Background())
	})

	root := startHTTPRun(t, httpServer.URL, "server-scope-mainAgent", `{"message":"research and report","turn_id":"server-scope-root"}`)
	getSSEEvents(t, httpServer.URL+root.EventsURL, "")
	select {
	case message := <-delivered:
		t.Fatalf("report handler ran before result: %q", message)
	case <-time.After(20 * time.Millisecond):
	}

	subagentModel.waitRequests(t, 1)
	subagentModel.release()
	subagentModel.waitRequests(t, 2)
	subagentModel.release()
	events := getSessionEventsUntil(t, httpServer.URL+root.SessionEventsURL, "", func(event SessionEventResponse) bool {
		return event.ScopeEvent != nil &&
			event.ScopeEvent.Type == EndScope &&
			event.ScopeEvent.ScopeID == root.TurnID
	})

	resultTurnID := ""
	resultScopeSeen := false
	for _, event := range events {
		if event.Source != ServerTurnSourceSubagentResult || event.SubagentResult == nil {
			continue
		}
		resultTurnID = event.TurnID
		if event.SubagentResult.MainAgentSessionID != root.SessionID ||
			event.SubagentResult.MainAgentTurnID != root.TurnID {
			t.Fatalf("result correlation = %#v", event.SubagentResult)
		}
		if event.ScopeEvent != nil &&
			event.ScopeEvent.ScopeID == root.TurnID &&
			event.ScopeEvent.TriggerTurnID == resultTurnID {
			resultScopeSeen = true
		}
	}
	if resultTurnID == "" || !resultScopeSeen {
		t.Fatalf("result was not bound to root response scope: %#v", events)
	}
	select {
	case message := <-delivered:
		if message != "final verified response" {
			t.Fatalf("delivered message = %q, want latest result report", message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server after-scope handler")
	}
	select {
	case duplicate := <-delivered:
		t.Fatalf("handler ran more than once: %q", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSubagentIntegrationAutoClosesAfterUserVisibleResultAnswer(t *testing.T) {
	mainAgentModel := &continuingCloseResultMainAgentModel{}
	subagentModel := newIntegrationCompletedSubagentModel("verified subagent result", "The delegated work completed.")
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{"researcher": subagentModel})
	results := agent.SubscribeSubagentResults(context.Background())

	mainAgentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent", TurnID: "mainAgent-close-continuation",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate and close when complete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, mainAgentRun)
	subagentModel.waitRequests(t, 1)
	subagentModel.release()
	subagentModel.waitRequests(t, 2)
	subagentModel.release()

	var result SubagentResult
	select {
	case result = <-results:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subagent result")
	}
	continuation, subscription, err := agent.ContinueSubagentResultSubscribed(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	for range subscription.Events {
	}
	waitRun(t, continuation)
	runResult, err := continuation.Result()
	if err != nil {
		t.Fatal(err)
	}
	if runResult.Content != "The delegated work completed. Verified subagent result." {
		t.Fatalf("result delivery result = %#v", runResult)
	}
	if continuation.CompletionRepairCount() != 0 {
		t.Fatalf("result delivery repairs = %d, want 0", continuation.CompletionRepairCount())
	}
	awaitSubagentStatus(t, agent.subagents, result.SubagentID, storage.SubagentStatusClosed)
	requests := mainAgentModel.Requests()
	if len(requests) != 3 {
		t.Fatalf("mainAgent provider requests = %d, want start, post-start completion, result delivery", len(requests))
	}
	continuationRequest := requests[2]
	if len(continuationRequest.Tools) == 0 || strings.Contains(integrationReminderContents(continuationRequest.ContextReminders), "no user-visible assistant response") {
		t.Fatalf("normal continuation request = %#v", continuationRequest)
	}
}

func TestSubagentIntegrationRepairsMissingOutcomeWithoutRepeatingDomainTool(t *testing.T) {
	mainAgentModel := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "research", Name: StartSubagentToolName,
		Arguments: map[string]any{"name": "researcher", "message": "perform the domain action", "continue_main_agent": true},
	}}}
	subagentModel := &outcomeRepairIntegrationModel{}
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{
		"researcher": newIntegrationSubagentModel("unused"),
	})
	var domainMu sync.Mutex
	domainCalls := 0
	agent.subagents.subagentFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(),
			withSubagentAgent(),
			WithModel(subagentModel),
			WithMessageStorage(agent.messages),
			WithTool(toolexecution.Tool{
				Definition: agentruntime.ToolDefinition{
					Name: "domain_action", InputSchema: agentruntime.ToolSchema{Type: "object"},
				},
				Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
					domainMu.Lock()
					domainCalls++
					domainMu.Unlock()
					return json.RawMessage(`{"performed":true}`), nil
				},
			}),
		)
	}
	results := agent.SubscribeSubagentResults(context.Background())

	mainAgentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent", TurnID: "mainAgent-repair-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate an action"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, mainAgentRun)

	var result SubagentResult
	select {
	case result = <-results:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for repaired subagent result")
	}
	if result.Status != SubagentResultCompleted || result.Summary != "The domain action completed once." {
		t.Fatalf("result = %#v", result)
	}
	if result.FinalAnswer == nil || result.FinalAnswer.Content != "The domain action is complete." {
		t.Fatalf("final answer = %#v", result.FinalAnswer)
	}
	domainMu.Lock()
	gotDomainCalls := domainCalls
	domainMu.Unlock()
	if gotDomainCalls != 1 {
		t.Fatalf("domain tool calls = %d, want exactly 1", gotDomainCalls)
	}
	requests := subagentModel.Requests()
	if len(requests) != 4 {
		t.Fatalf("subagent provider requests = %d, want domain, answer, repair, final: %#v", len(requests), requests)
	}
	for index := 2; index < len(requests); index++ {
		foundOutcome := false
		for _, tool := range requests[index].Tools {
			if tool.Name == toolexecution.SubagentResultToolName {
				foundOutcome = true
				break
			}
		}
		if !foundOutcome {
			t.Fatalf("request %d tools = %#v, want outcome tool available", index, requests[index].Tools)
		}
	}
	if !hasSubagentReportRepairReminder(requests[2]) {
		t.Fatalf("repair reminder = %#v", requests[2].ContextReminders)
	}
	subagentRun, err := agent.SubagentRun(context.Background(), "mainAgent", result.SubagentID, result.SubagentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if subagentRun.CompletionRepairCount() != 1 {
		t.Fatalf("subagent repair count = %d, want 1", subagentRun.CompletionRepairCount())
	}
	select {
	case duplicate := <-results:
		t.Fatalf("duplicate result = %#v", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSubagentIntegrationMissingOutcomeFallsBackAfterBoundedRepairs(t *testing.T) {
	mainAgentModel := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "research", Name: StartSubagentToolName,
		Arguments: map[string]any{"name": "researcher", "message": "answer without reporting", "continue_main_agent": true},
	}}}
	subagentModel := &scriptedModel{}
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{
		"researcher": newIntegrationSubagentModel("unused"),
	})
	agent.subagents.subagentFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(), withSubagentAgent(), WithModel(subagentModel), WithMessageStorage(agent.messages))
	}
	results := agent.SubscribeSubagentResults(context.Background())
	mainAgentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent", TurnID: "mainAgent-fallback-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate incomplete reporting"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, mainAgentRun)

	select {
	case result := <-results:
		if result.Status != SubagentResultIncomplete || result.Summary != "Subagent turn ended without a successful result report after bounded repair attempts." {
			t.Fatalf("fallback result = %#v", result)
		}
		if got := len(subagentModel.Requests()); got != defaultCompletionRepairLimit+1 {
			t.Fatalf("provider requests = %d, want initial plus bounded repairs", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fallback subagent result")
	}
}

func TestSubagentIntegrationExplicitInterruptStopsSubagentAfterMainAgentTurnEnds(t *testing.T) {
	subagentModel := newIntegrationSubagentModel("never completes without release")
	mainAgentModel := &integrationInterruptMainAgentModel{}
	agent := newIntegrationSubagentAgent(t, mainAgentModel, map[string]*integrationSubagentModel{"researcher": subagentModel})

	mainAgentRun, err := agent.Start(context.Background(), agentruntime.Request{
		SessionID: "mainAgent", TurnID: "mainAgent-turn",
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "delegate then wait"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, mainAgentRun)
	subagentModel.waitRequests(t, 1)
	subagents, err := agent.ListSubagents(context.Background(), "mainAgent", false)
	if err != nil || len(subagents) != 1 {
		t.Fatalf("subagent before mainAgent interrupt = %#v, err = %v", subagents, err)
	}
	subagentRun, err := agent.SubagentRun(context.Background(), "mainAgent", subagents[0].ID, subagents[0].CurrentSubagentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.InterruptSubagent(context.Background(), "mainAgent", subagents[0].ID, "mainAgent cancelled"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, subagentRun)
	if _, err := subagentRun.Result(); !errors.Is(err, agentruntime.ErrRunInterrupted) {
		t.Fatalf("subagent result after mainAgent interrupt = %v, want ErrRunInterrupted", err)
	}
	awaitSubagentStatus(t, agent.subagents, subagents[0].ID, storage.SubagentStatusIdle)
}

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
	if got := mainAgentModel.Requests()[0].ContextReminders; len(got) != 1 ||
		!strings.Contains(got[0].Content, "<turn_start>") {
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
	if len(reminders) != 2 ||
		!strings.Contains(reminders[0].Content, "<turn_start>") ||
		!strings.Contains(reminders[1].Content, created.ID) ||
		!strings.Contains(reminders[1].Content, "<active_subagents>") {
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
	if response.StatusCode != http.StatusAccepted {
		defer response.Body.Close()
		t.Fatalf("send HTTP subagent status = %d", response.StatusCode)
	}
	response.Body.Close()
	subagentModel.waitRequests(t, 2)
	subagentModel.release()
	awaitSubagentStatus(t, agent.subagents, created.ID, storage.SubagentStatusIdle)
	observeTestSubagentResult(t, agent.subagents, markTestSubagentCompleted(t, agent.subagents, created.ID))

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
	if got := mainAgentModel.Requests()[2].ContextReminders; len(got) != 1 ||
		!strings.Contains(got[0].Content, "<turn_start>") ||
		strings.Contains(got[0].Content, "<active_subagents>") {
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
		providerName: "test", modelName: "mainAgent-model",
		subagents: definitions,
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
	mu        sync.Mutex
	requests  []agentruntime.ModelRequest
	releaseC  chan struct{}
	content   string
	outcome   *toolexecution.SubagentReport
	repairing bool
}

type outcomeRepairIntegrationModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
}

type continuingCloseResultMainAgentModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
}

type responseScopeIntegrationMainAgentModel struct {
	mu       sync.Mutex
	requests int
}

func (m *responseScopeIntegrationMainAgentModel) Start(_ context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	index := m.requests
	m.requests++
	m.mu.Unlock()
	var calls []provider.ToolCall
	switch index {
	case 0:
		calls = []provider.ToolCall{{
			ID: "start", Name: StartSubagentToolName,
			Arguments: map[string]any{"name": "researcher", "message": "research", "continue_main_agent": true},
		}}
	case 1:
		calls = []provider.ToolCall{{ID: "report-waiting", Name: "report", Arguments: map[string]any{"message": "waiting response"}}}
	case 2:
		return scriptedStream{result: provider.StreamResult{Content: "work completed for this turn", Finished: true}}, nil
	default:
		calls = []provider.ToolCall{{ID: "report-final", Name: "report", Arguments: map[string]any{"message": "final verified response"}}}
	}
	return scriptedStream{result: provider.StreamResult{CompletedTools: calls, Finished: true}}, nil
}

func (m *responseScopeIntegrationMainAgentModel) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

func (m *continuingCloseResultMainAgentModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	var result provider.StreamResult
	switch index {
	case 0:
		result = provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "start-subagent", Name: StartSubagentToolName,
			Arguments: map[string]any{"name": "researcher", "message": "verify the delegated result", "continue_main_agent": true},
		}}, Finished: true}
	case 1:
		result = provider.StreamResult{Content: "Delegation is running asynchronously.", Finished: true}
	default:
		result = provider.StreamResult{Content: "The delegated work completed. Verified subagent result.", Finished: true}
	}
	return scriptedStream{result: result}, nil
}

func (m *continuingCloseResultMainAgentModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}

func integrationReminderContents(reminders []agentruntime.ContextReminder) string {
	var contents []string
	for _, reminder := range reminders {
		contents = append(contents, reminder.Content)
	}
	return strings.Join(contents, "\n")
}

func (m *outcomeRepairIntegrationModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	var result provider.StreamResult
	switch index {
	case 0:
		result = provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "domain-call", Name: "domain_action", Arguments: map[string]any{},
		}}, Finished: true}
	case 1:
		result = provider.StreamResult{Content: "The action ran, but I omitted the outcome report.", Finished: true}
	case 2:
		result = provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "outcome-call", Name: toolexecution.SubagentResultToolName,
			Arguments: map[string]any{
				"status":  string(toolexecution.SubagentReportCompleted),
				"summary": "The domain action completed once.",
			},
		}}, Finished: true}
	default:
		result = provider.StreamResult{Content: "The domain action is complete.", Finished: true}
	}
	return scriptedStream{result: result}, nil
}

func (m *outcomeRepairIntegrationModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}

func newIntegrationSubagentModel(content string) *integrationSubagentModel {
	return &integrationSubagentModel{releaseC: make(chan struct{}, 8), content: content}
}

func newIntegrationCompletedSubagentModel(content, summary string) *integrationSubagentModel {
	return &integrationSubagentModel{
		releaseC: make(chan struct{}, 8), content: content,
		outcome: &toolexecution.SubagentReport{Status: toolexecution.SubagentReportCompleted, Summary: summary},
	}
}

func (m *integrationSubagentModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	if hasSubagentReportRepairReminder(request) {
		m.repairing = true
	}
	repairing := m.repairing
	m.mu.Unlock()
	if hasSubagentReportRepairReminder(request) {
		return scriptedStream{result: provider.StreamResult{
			CompletedTools: []provider.ToolCall{{
				ID:   "outcome-repair",
				Name: toolexecution.SubagentResultToolName,
				Arguments: map[string]any{
					"status":    string(toolexecution.SubagentReportIncomplete),
					"summary":   m.content,
					"next_step": "Continue with more information.",
				},
			}},
			Finished: true,
		}}, nil
	}
	if repairing {
		if _, found := reportedSubagentReport(request.TurnID, request.Messages); found {
			return scriptedStream{result: provider.StreamResult{Content: m.content, Finished: true}}, nil
		}
	}
	stream := integrationSubagentStream{releaseC: m.releaseC, content: m.content}
	if m.outcome != nil {
		_, reported := reportedSubagentReport(request.TurnID, request.Messages)
		if reported {
			return stream, nil
		}
		arguments := map[string]any{"status": string(m.outcome.Status), "summary": m.outcome.Summary}
		if m.outcome.NextStep != "" {
			arguments["next_step"] = m.outcome.NextStep
		}
		stream.content = ""
		stream.toolCalls = []provider.ToolCall{{ID: "outcome", Name: toolexecution.SubagentResultToolName, Arguments: arguments}}
	}
	return stream, nil
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
	releaseC  <-chan struct{}
	content   string
	toolCalls []provider.ToolCall
}

func (s integrationSubagentStream) Subscribe(ctx context.Context) <-chan provider.StreamEvent {
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

func (integrationSubagentStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, errors.New("unused")
}

type integrationInterruptMainAgentModel struct {
	mu     sync.Mutex
	starts int
}

type integrationSequentialAssignmentMainAgentModel struct {
	mu       sync.Mutex
	requests int
}

type integrationPendingResultMainAgentModel struct {
	mu            sync.Mutex
	requests      int
	secondEntered chan struct{}
	secondRelease chan struct{}
}

func newIntegrationPendingResultMainAgentModel() *integrationPendingResultMainAgentModel {
	return &integrationPendingResultMainAgentModel{
		secondEntered: make(chan struct{}),
		secondRelease: make(chan struct{}),
	}
}

func (m *integrationPendingResultMainAgentModel) Start(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	m.requests++
	round := m.requests
	m.mu.Unlock()

	if round == 1 {
		call := provider.ToolCall{ID: "start", Name: StartSubagentToolName, Arguments: map[string]any{
			"name": "researcher", "message": "ask for the missing transfer details", "continue_main_agent": true,
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
		subagentID := integrationStartedSubagentID(request.Messages)
		if subagentID == "" {
			return nil, errors.New("start_subagent result did not contain a subagent ID")
		}
		call := provider.ToolCall{ID: "send", Name: SendSubagentMessageToolName, Arguments: map[string]any{
			"subagent_id": subagentID, "message": "ask again", "continue_main_agent": true,
		}}
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{call}, Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{Finished: true}}, nil
}

func (m *integrationPendingResultMainAgentModel) waitForSecondRound(t *testing.T) {
	t.Helper()
	select {
	case <-m.secondEntered:
	case <-time.After(time.Second):
		t.Fatal("mainAgent did not reach second provider round")
	}
}

func (m *integrationPendingResultMainAgentModel) releaseSecondRound() {
	close(m.secondRelease)
}

func (m *integrationPendingResultMainAgentModel) requestCount() int {
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

func (m *integrationSequentialAssignmentMainAgentModel) Start(_ context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	m.requests++
	round := m.requests
	m.mu.Unlock()

	call := provider.ToolCall{ID: "research", Name: StartSubagentToolName, Arguments: map[string]any{
		"name": "researcher", "message": "research this", "continue_main_agent": true,
	}}
	if round == 2 {
		call = provider.ToolCall{ID: "review", Name: StartSubagentToolName, Arguments: map[string]any{
			"name": "reviewer", "message": "review this", "continue_main_agent": true,
		}}
	}
	if round > 2 {
		return scriptedStream{result: provider.StreamResult{Content: "delegation complete", Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{call}, Finished: true}}, nil
}

func (m *integrationSequentialAssignmentMainAgentModel) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

func (m *integrationInterruptMainAgentModel) Start(_ context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	if m.starts == 1 {
		return scriptedStream{result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "delegate", Name: StartSubagentToolName, Arguments: map[string]any{"name": "researcher", "message": "long task", "continue_main_agent": true}}}, Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{Content: "Delegation is running asynchronously.", Finished: true}}, nil
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
