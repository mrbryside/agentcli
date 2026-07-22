package agentcli

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"harness-api/agentruntime"
	"harness-api/storage"
	"harness-api/storage/inmemory"
	"harness-api/toolexecution"
)

type skillLoader struct {
	loader *toolexecution.SkillLoader
}

type skillToolResult = toolexecution.SkillToolResult

func newSkillLoader(project *Project, messages storage.MessageStorage, policy SkillReloadPolicy) *skillLoader {
	return &skillLoader{loader: toolexecution.NewSkillLoader(project.executionSkills(), messages, policy)}
}

func (loader *skillLoader) tool() toolexecution.Tool {
	return loader.loader.Tool()
}

func (loader *skillLoader) handle(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	return loader.tool().Handler(ctx, arguments)
}

func TestSkillLoaderDeduplicatesRecentInstructions(t *testing.T) {
	project := skillLoaderProject("Follow the testing instructions.")
	messages := inmemory.NewMessageStorage()
	loader := newSkillLoader(project, messages, DefaultSkillReloadPolicy())

	loaded := callSkillLoader(t, loader, "session", "turn-1", "call-1")
	if loaded.Status != "loaded" || loaded.Instructions == "" {
		t.Fatalf("first result = %#v", loaded)
	}
	appendSkillResult(t, messages, "session", "turn-1", "result-1", "call-1", loaded)
	appendUserMessage(t, messages, "session", "turn-2", "user-2", "please test this")

	recent := callSkillLoader(t, loader, "session", "turn-2", "call-2")
	if recent.Status != "already_loaded" || recent.Instructions != "" || !recent.DoNotCallAgainThisTurn || !strings.Contains(recent.NextAction, "unavailable") {
		t.Fatalf("recent result = %#v", recent)
	}
}

func TestSkillLoaderRefreshesByTurnDistance(t *testing.T) {
	project := skillLoaderProject("Follow the testing instructions.")
	messages := inmemory.NewMessageStorage()
	loader := newSkillLoader(project, messages, SkillReloadPolicy{MaxTurnDistance: 1})

	loaded := callSkillLoader(t, loader, "session", "turn-1", "call-1")
	appendSkillResult(t, messages, "session", "turn-1", "result-1", "call-1", loaded)
	appendUserMessage(t, messages, "session", "turn-2", "user-2", "test again")

	refreshed := callSkillLoader(t, loader, "session", "turn-2", "call-2")
	if refreshed.Status != "loaded" || refreshed.Instructions == "" || !strings.Contains(refreshed.Reason, "turn distance") {
		t.Fatalf("refreshed result = %#v", refreshed)
	}
}

func TestSkillLoaderRefreshesByTokenDistance(t *testing.T) {
	project := skillLoaderProject("Follow the testing instructions.")
	messages := inmemory.NewMessageStorage()
	loader := newSkillLoader(project, messages, SkillReloadPolicy{MaxTokenDistance: 20})

	loaded := callSkillLoader(t, loader, "session", "turn-1", "call-1")
	appendSkillResult(t, messages, "session", "turn-1", "result-1", "call-1", loaded)
	appendUserMessage(t, messages, "session", "turn-2", "user-2", strings.Repeat("context ", 100))

	refreshed := callSkillLoader(t, loader, "session", "turn-2", "call-2")
	if refreshed.Status != "loaded" || !strings.Contains(refreshed.Reason, "token distance") {
		t.Fatalf("refreshed result = %#v", refreshed)
	}
}

func TestSkillLoaderRefreshesChangedContent(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	oldLoader := newSkillLoader(skillLoaderProject("Old instructions."), messages, DefaultSkillReloadPolicy())
	loaded := callSkillLoader(t, oldLoader, "session", "turn-1", "call-1")
	appendSkillResult(t, messages, "session", "turn-1", "result-1", "call-1", loaded)
	appendUserMessage(t, messages, "session", "turn-2", "user-2", "test again")

	newLoader := newSkillLoader(skillLoaderProject("New instructions."), messages, DefaultSkillReloadPolicy())
	refreshed := callSkillLoader(t, newLoader, "session", "turn-2", "call-2")
	if refreshed.Status != "loaded" || refreshed.Instructions != "New instructions." || refreshed.Reason != "skill content changed" {
		t.Fatalf("refreshed result = %#v", refreshed)
	}
}

func TestSkillLoaderDeduplicatesParallelSameTurnCalls(t *testing.T) {
	loader := newSkillLoader(skillLoaderProject("Instructions."), inmemory.NewMessageStorage(), DefaultSkillReloadPolicy())
	start := make(chan struct{})
	results := make(chan skillToolResult, 2)
	var workers sync.WaitGroup
	for _, callID := range []string{"call-1", "call-2"} {
		workers.Add(1)
		go func(callID string) {
			defer workers.Done()
			<-start
			results <- callSkillLoader(t, loader, "session", "turn", callID)
		}(callID)
	}
	close(start)
	workers.Wait()
	close(results)

	statuses := map[string]int{}
	for result := range results {
		statuses[result.Status]++
	}
	if statuses["loaded"] != 1 || statuses["already_loaded"] != 1 {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestSkillLoaderRequiresInvocationContext(t *testing.T) {
	tool := newSkillLoader(skillLoaderProject("Instructions."), inmemory.NewMessageStorage(), DefaultSkillReloadPolicy()).tool()
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"testing-go"}`)); err == nil {
		t.Fatal("load_skill accepted a handler call without invocation context")
	}
}

func skillLoaderProject(instructions string) *Project {
	skill := Skill{Name: "testing-go", Description: "Use when testing Go.", Instructions: instructions}
	return &Project{skills: map[string]Skill{skill.Name: skill}}
}

func callSkillLoader(t *testing.T, loader *skillLoader, sessionID, turnID, callID string) skillToolResult {
	t.Helper()
	ctx := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: sessionID, TurnID: turnID, CallID: callID, ToolName: SkillLoaderToolName,
	})
	output, err := loader.handle(ctx, json.RawMessage(`{"name":"testing-go"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result skillToolResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func appendSkillResult(t *testing.T, messages *inmemory.MessageStorage, sessionID, turnID, messageID, callID string, result skillToolResult) {
	t.Helper()
	output, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	err = messages.Append(context.Background(), agentruntime.Message{
		ID: messageID, SessionID: sessionID, TurnID: turnID, Type: agentruntime.MessageTypeToolResult,
		ToolResult: &agentruntime.ToolResult{
			CallID: callID, Name: SkillLoaderToolName, Status: agentruntime.ToolResultSucceeded, Output: output,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func appendUserMessage(t *testing.T, messages *inmemory.MessageStorage, sessionID, turnID, messageID, content string) {
	t.Helper()
	if err := messages.Append(context.Background(), agentruntime.Message{
		ID: messageID, SessionID: sessionID, TurnID: turnID, Type: agentruntime.MessageTypeUser, Content: content,
	}); err != nil {
		t.Fatal(err)
	}
}
