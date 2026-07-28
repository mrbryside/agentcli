package toolexecution

import (
	"context"
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

func TestExecutorBlocksToolUntilEveryRequiredSkillLoadedInCurrentTurn(t *testing.T) {
	var executed atomic.Int32
	registry := NewRegistry()
	if err := registry.Register(Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        "web_search",
			Description: "Search the web.",
			InputSchema: agentruntime.ToolSchema{Type: "object"},
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			executed.Add(1)
			return json.RawMessage(`{"results":[]}`), nil
		},
		RequiredSkills: []string{"web-research", "source-policy"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecutor(registry, 1); err == nil {
		t.Fatal("NewExecutor() without message storage succeeded for required-skill tool")
	}

	messages := inmemory.NewMessageStorage()
	appendSkillLoadResult(t, messages, "session", "prior-turn", "prior", SkillToolResult{
		Status: "loaded", Name: "web-research", Instructions: "Older instructions.",
	})
	executor, err := NewExecutor(registry, 1, Config{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan agentruntime.ToolRequest, 2)
	results := make(chan agentruntime.ToolResultEnvelope, 2)
	interrupts := make(chan agentruntime.ToolInterrupt, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runExecutor(executor, ctx, requests, results, interrupts)

	requests <- toolRequest("session", "current-turn", "blocked", "web_search", `{}`)
	blocked := waitResult(t, results)
	if got := executed.Load(); got != 0 {
		t.Fatalf("handler executions = %d, want 0", got)
	}
	if blocked.Result.Status != agentruntime.ToolResultSucceeded ||
		blocked.Result.TriggerSatisfied == nil ||
		*blocked.Result.TriggerSatisfied {
		t.Fatalf("blocked result = %+v", blocked.Result)
	}
	var output struct {
		Status         string   `json:"status"`
		Executed       bool     `json:"executed"`
		Reason         string   `json:"reason"`
		RequiredSkills []string `json:"required_skills"`
		MissingSkills  []string `json:"missing_skills"`
		Instruction    string   `json:"instruction"`
	}
	if err := json.Unmarshal(blocked.Result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Status != "blocked" ||
		output.Executed ||
		output.Reason != "required_skill_not_loaded" ||
		!reflect.DeepEqual(output.RequiredSkills, []string{"web-research", "source-policy"}) ||
		!reflect.DeepEqual(output.MissingSkills, []string{"web-research", "source-policy"}) ||
		output.Instruction == "" {
		t.Fatalf("blocked output = %+v", output)
	}

	appendSkillLoadResult(t, messages, "session", "current-turn", "current-web", SkillToolResult{
		Status: "loaded", Name: "web-research", InstructionsInContext: true,
	})
	appendSkillLoadResult(t, messages, "session", "current-turn", "current-policy", SkillToolResult{
		Status: "loaded", Name: "source-policy", Instructions: "Apply source policy.",
	})
	requests <- toolRequest("session", "current-turn", "allowed", "web_search", `{}`)
	allowed := waitResult(t, results)
	if allowed.Result.Status != agentruntime.ToolResultSucceeded ||
		string(allowed.Result.Output) != `{"results":[]}` {
		t.Fatalf("allowed result = %+v", allowed.Result)
	}
	if got := executed.Load(); got != 1 {
		t.Fatalf("handler executions = %d, want 1", got)
	}

	cancel()
	waitDone(t, done)
}

func TestMissingRequiredSkillsIgnoresMalformedAndFailedLoadResults(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	appendSkillLoadMessage(t, messages, storage.Message{
		ID:        "failed",
		SessionID: "session",
		TurnID:    "turn",
		Type:      storage.MessageTypeToolResult,
		ToolResult: &storage.ToolResult{
			CallID: "failed",
			Name:   SkillLoaderToolName,
			Status: storage.ToolResultFailed,
			Output: json.RawMessage(`{"status":"loaded","name":"web-research"}`),
			Error:  "load failed",
		},
	})
	appendSkillLoadMessage(t, messages, storage.Message{
		ID:        "malformed",
		SessionID: "session",
		TurnID:    "turn",
		Type:      storage.MessageTypeToolResult,
		ToolResult: &storage.ToolResult{
			CallID: "malformed",
			Name:   SkillLoaderToolName,
			Status: storage.ToolResultSucceeded,
			Output: json.RawMessage(`{"status":"unknown","name":"web-research"}`),
		},
	})

	missing, err := missingRequiredSkills(
		context.Background(),
		messages,
		"session",
		"turn",
		[]string{"web-research"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(missing, []string{"web-research"}) {
		t.Fatalf("missingRequiredSkills() = %v", missing)
	}
}

func appendSkillLoadResult(
	t *testing.T,
	messages storage.MessageStorage,
	sessionID string,
	turnID string,
	id string,
	result SkillToolResult,
) {
	t.Helper()
	output, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	appendSkillLoadMessage(t, messages, storage.Message{
		ID:        id,
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      storage.MessageTypeToolResult,
		ToolResult: &storage.ToolResult{
			CallID: id,
			Name:   SkillLoaderToolName,
			Status: storage.ToolResultSucceeded,
			Output: output,
		},
	})
}

func appendSkillLoadMessage(t *testing.T, messages storage.MessageStorage, message storage.Message) {
	t.Helper()
	if err := messages.Append(context.Background(), message); err != nil {
		t.Fatal(err)
	}
}
