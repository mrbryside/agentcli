package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
)

func TestCompactorPrepareBelowThresholdIsNoop(t *testing.T) {
	request := compactionRequest(compactionMessage("one", MessageTypeUser, "hello"))
	result, err := (Compactor{}).Prepare(context.Background(), CompactionInput{Request: request, MainModelMetadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512}})
	if err != nil || result.Compacted || result.Checkpoint != nil {
		t.Fatalf("Prepare() = %#v, %v", result, err)
	}
	if got := result.Request.Messages[0].ID; got != "one" {
		t.Fatalf("message ID = %q", got)
	}
	if result.Request.MaxOutputTokens != 512 {
		t.Fatalf("max output tokens = %d", result.Request.MaxOutputTokens)
	}
}

func TestDeriveCompactionBudgetsUsesDynamicTailReserves(t *testing.T) {
	metadata := ModelMetadata{
		ContextWindowTokens: 122880,
		MaxOutputTokens:     66560,
	}
	output := operationalMaxOutputTokens(0, metadata)
	budgets := deriveCompactionBudgets(metadata, output)
	if output != 15360 || budgets.input != 107520 || budgets.summary != 4096 || budgets.safety != 4096 || budgets.usableInput() != 103424 {
		t.Fatalf("budgets = %#v; usable input = %d", budgets, budgets.usableInput())
	}
	if got := recentTailBudget(budgets.usableInput()); got != 8192 {
		t.Fatalf("recent tail budget = %d", got)
	}
}

func TestCompactorOperationalOutputCapHonorsOnlyLowerRequestLimit(t *testing.T) {
	metadata := ModelMetadata{ContextWindowTokens: 122880, MaxOutputTokens: 66560}
	tests := []struct {
		name      string
		requested int
		want      int
	}{
		{name: "default cap", want: 15360},
		{name: "lower request", requested: 8000, want: 8000},
		{name: "higher request remains capped", requested: 50000, want: 15360},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := compactionRequest(compactionMessage("one", MessageTypeUser, "hello"))
			request.MaxOutputTokens = test.requested
			result, err := (Compactor{}).Prepare(context.Background(), CompactionInput{
				Request:           request,
				MainModelMetadata: metadata,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Request.MaxOutputTokens != test.want {
				t.Fatalf("max output tokens = %d; want %d", result.Request.MaxOutputTokens, test.want)
			}
			budgets := deriveCompactionBudgets(metadata, result.Request.MaxOutputTokens)
			if budgets.input != metadata.ContextWindowTokens-test.want {
				t.Fatalf("input budget = %d; want %d", budgets.input, metadata.ContextWindowTokens-test.want)
			}
		})
	}
}

func TestCompactorPrepareProjectsExistingCheckpointWithoutResummarizing(t *testing.T) {
	old := compactionMessage("old", MessageTypeUser, "old")
	tail := compactionMessage("tail", MessageTypeUser, "current")
	checkpoint := compactionCheckpoint("cp", "old", "tail", "existing memory")
	request := compactionRequest(old, tail, checkpoint)
	result, err := (Compactor{}).Prepare(context.Background(), CompactionInput{Request: request, MainModelMetadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512}})
	if err != nil || result.Compacted {
		t.Fatalf("Prepare() = %#v, %v", result, err)
	}
	if len(result.Request.Messages) != 2 || !strings.Contains(result.Request.Messages[0].Content, "existing memory") || result.Request.Messages[1].ID != "tail" {
		t.Fatalf("projection = %#v", result.Request.Messages)
	}
}

func TestCompactorPrepareCreatesCumulativeCheckpointAndUsesToolFreeModel(t *testing.T) {
	old := compactionMessage("old", MessageTypeUser, strings.Repeat("old ", 500))
	call := compactionMessage("call", MessageTypeToolCall, "")
	call.ToolCalls = []ToolCall{{CallID: "c", Name: "tool", Arguments: []byte(`{}`)}}
	resultMessage := compactionMessage("result", MessageTypeToolResult, "")
	resultMessage.ToolResult = &ToolResult{CallID: "c", Name: "tool", Status: ToolResultSucceeded, Output: []byte(`{"value":"ok"}`)}
	latest := compactionMessage("latest", MessageTypeUser, "what next?")
	model := &limitedCompactionModel{compactionModel: compactionModel{content: "durable memory"}, metadata: ModelMetadata{ContextWindowTokens: 2048, MaxOutputTokens: 64}}
	result, err := (Compactor{Model: model}).Prepare(context.Background(), CompactionInput{Request: compactionRequest(old, call, resultMessage, latest), MainModelMetadata: ModelMetadata{ContextWindowTokens: 500, MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || result.Checkpoint == nil || result.Checkpoint.Summary != "durable memory" {
		t.Fatalf("result = %#v", result)
	}
	if len(model.request.Tools) != 0 || model.request.MaxOutputTokens != 64 || !strings.Contains(model.request.Messages[0].Content, "<history_to_merge>") || !strings.Contains(model.request.Messages[0].Content, "<previous_summary>") || !strings.Contains(model.request.Messages[0].Content, "# Objective") || !strings.Contains(model.request.Messages[0].Content, "## Completed") || !strings.Contains(model.request.Messages[0].Content, "exact file paths and exact IDs") || !strings.Contains(model.request.Messages[0].Content, "primary language") {
		t.Fatalf("summary request = %#v", model.request)
	}
	if result.Request.Messages[len(result.Request.Messages)-1].ID != "latest" {
		t.Fatalf("latest tail not retained: %#v", result.Request.Messages)
	}
}

func TestCompactorPrepareRejectsMalformedCheckpoint(t *testing.T) {
	request := compactionRequest(compactionMessage("one", MessageTypeUser, "one"), compactionCheckpoint("cp", "missing", "one", "memory"))
	_, err := (Compactor{}).Prepare(context.Background(), CompactionInput{Request: request, MainModelMetadata: ModelMetadata{ContextWindowTokens: 1000, MaxOutputTokens: 100}})
	if !errors.Is(err, ErrInvalidCompactionCheckpoint) {
		t.Fatalf("error = %v", err)
	}
}

func TestCompactorToolUnitsAndSerializationPreserveAdjacencyAndBoundOutput(t *testing.T) {
	call := compactionMessage("call", MessageTypeToolCall, "")
	call.ToolCalls = []ToolCall{{CallID: "c", Name: "tool", Arguments: []byte(`{}`)}}
	result := compactionMessage("result", MessageTypeToolResult, "")
	result.ToolResult = &ToolResult{CallID: "c", Name: "tool", Status: ToolResultSucceeded, Output: []byte(`"` + strings.Repeat("x", 1000) + `"`)}
	units := conversationUnits([]Message{call, result, compactionMessage("next", MessageTypeUser, "next")})
	if len(units) != 2 || len(units[0]) != 2 {
		t.Fatalf("units = %#v", units)
	}
	serialized, err := serializeHistory([]Message{call, result}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(serialized) > 2000 || !strings.Contains(serialized, "call") || !strings.Contains(serialized, `"truncated":true`) || !strings.Contains(serialized, `"CallID":"c"`) {
		t.Fatalf("serialized = %q", serialized)
	}
}

func TestCompactorSummarizesEveryHeadChunkCumulatively(t *testing.T) {
	first := compactionMessage("first", MessageTypeUser, strings.Repeat("first ", 350))
	second := compactionMessage("second", MessageTypeAssistant, strings.Repeat("second ", 350))
	latest := compactionMessage("latest", MessageTypeUser, "continue")
	model := &compactionModel{content: "cumulative summary"}
	started := 0
	result, err := (Compactor{Model: model}).PrepareWithHooks(context.Background(), CompactionInput{Request: compactionRequest(first, second, latest), MainModelMetadata: ModelMetadata{ContextWindowTokens: 1000, MaxOutputTokens: 100}}, CompactionHooks{Started: func() { started++ }})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || started != 1 || len(model.requests) != 2 {
		t.Fatalf("result=%#v started=%d calls=%d", result, started, len(model.requests))
	}
	if !strings.Contains(model.requests[0].Messages[0].Content, "first") || !strings.Contains(model.requests[1].Messages[0].Content, "second") {
		t.Fatalf("chunks dropped history: %#v", model.requests)
	}
	if !strings.Contains(model.requests[1].Messages[0].Content, "cumulative summary") {
		t.Fatalf("second chunk did not merge prior summary: %q", model.requests[1].Messages[0].Content)
	}
}

func TestCompactionHistoryRejectsOversizedNonToolUnit(t *testing.T) {
	_, err := compactionHistoryChunks([]Message{compactionMessage("huge", MessageTypeUser, strings.Repeat("x", 5000))}, 100)
	if !errors.Is(err, ErrCompactionHistoryTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestLatestCheckpointRejectsGapsAndNonMonotonicRepeatedCheckpoints(t *testing.T) {
	one := compactionMessage("one", MessageTypeUser, "one")
	two := compactionMessage("two", MessageTypeAssistant, "two")
	three := compactionMessage("three", MessageTypeUser, "three")
	gap := compactionRequest(one, two, three, compactionCheckpoint("gap", "one", "three", "memory"))
	if _, err := ProjectCompactionCheckpoints(gap); !errors.Is(err, ErrInvalidCompactionCheckpoint) {
		t.Fatalf("gap error = %v", err)
	}
	first := compactionCheckpoint("first-cp", "one", "two", "memory")
	repeated := compactionCheckpoint("second-cp", "one", "two", "memory again")
	nonMonotonic := compactionRequest(one, two, first, three, repeated)
	if _, err := ProjectCompactionCheckpoints(nonMonotonic); !errors.Is(err, ErrInvalidCompactionCheckpoint) {
		t.Fatalf("repeated error = %v", err)
	}
}

func TestCompactorRecentTailStartsAtConversationBoundary(t *testing.T) {
	user := compactionMessage("user", MessageTypeUser, "do work")
	assistant := compactionMessage("assistant", MessageTypeAssistant, "done")
	tail := selectRecentTail(ModelRequest{}, conversationUnits([]Message{user, assistant}), 1000, 0, GenericContextEstimator{})
	if len(tail) != 2 || tail[0].Type != MessageTypeUser {
		t.Fatalf("tail = %#v", tail)
	}
	if orphan := selectRecentTail(ModelRequest{}, conversationUnits([]Message{assistant}), 1000, 0, GenericContextEstimator{}); orphan != nil {
		t.Fatalf("orphan assistant tail = %#v", orphan)
	}
}

func TestCompactorRecentTailUsesBoundedDefaultAndLeavesInputRoom(t *testing.T) {
	older := compactionMessage("older", MessageTypeUser, strings.Repeat("o", 20_000))
	recent := compactionMessage("recent", MessageTypeUser, strings.Repeat("r", 20_000))
	template := ModelRequest{
		SystemPrompts: []string{strings.Repeat("system ", 400)},
		Tools:         []ToolDefinition{{Name: "tool", Description: strings.Repeat("schema ", 400)}},
	}
	estimator := GenericContextEstimator{}
	tail := selectRecentTail(template, conversationUnits([]Message{older, recent}), 50_000, 4_096, estimator)
	if len(tail) != 1 || tail[0].ID != "recent" {
		t.Fatalf("tail = %#v", tail)
	}
	prefix, err := estimator.Estimate(template)
	if err != nil {
		t.Fatal(err)
	}
	projected := template.Clone()
	projected.Messages = append(storage.CloneMessages(template.Messages), tail...)
	total, err := estimator.Estimate(projected)
	if err != nil {
		t.Fatal(err)
	}
	if used := total.Tokens - prefix.Tokens; used > defaultCompactionRecentTailTokens {
		t.Fatalf("recent tail uses %d tokens; limit = %d", used, defaultCompactionRecentTailTokens)
	}
	if room := 50_000 - 4_096 - total.Tokens; room < 20_000 {
		t.Fatalf("remaining input room = %d; want at least 20000", room)
	}
}

func TestCompactorFallsBackToCompactingWithinActiveTurn(t *testing.T) {
	user := compactionMessage("user", MessageTypeUser, strings.Repeat("objective ", 20))
	firstCall := compactionMessage("first-call", MessageTypeToolCall, "")
	firstCall.ToolCalls = []ToolCall{{CallID: "first", Name: "fetch", Arguments: []byte(`{"url":"https://example.com/large"}`)}}
	firstResult := compactionMessage("first-result", MessageTypeToolResult, "")
	firstResult.ToolResult = &ToolResult{CallID: "first", Name: "fetch", Status: ToolResultSucceeded, Output: []byte(`"` + strings.Repeat("x", 6000) + `"`)}
	latestCall := compactionMessage("latest-call", MessageTypeToolCall, "")
	latestCall.ToolCalls = []ToolCall{{CallID: "latest", Name: "search", Arguments: []byte(`{"query":"next"}`)}}
	latestResult := compactionMessage("latest-result", MessageTypeToolResult, "")
	latestResult.ToolResult = &ToolResult{CallID: "latest", Name: "search", Status: ToolResultSucceeded, Output: []byte(`{"result":"small"}`)}
	request := compactionRequest(user, firstCall, firstResult, latestCall, latestResult)
	model := &compactionModel{content: "current-turn findings"}

	result, err := (Compactor{Model: model}).Prepare(context.Background(), CompactionInput{
		Request:           request,
		MainModelMetadata: ModelMetadata{ContextWindowTokens: 2000, MaxOutputTokens: 400},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || result.Checkpoint == nil {
		t.Fatalf("result = %#v", result)
	}
	if result.Checkpoint.CoversThroughMessageID != "first-result" || result.Checkpoint.TailStartMessageID != "latest-call" {
		t.Fatalf("checkpoint = %#v", result.Checkpoint)
	}
	if len(result.Request.Messages) != 4 ||
		result.Request.Messages[0].Type != MessageTypeSystem ||
		result.Request.Messages[1].Type != MessageTypeRuntimeEvent ||
		result.Request.Messages[1].Content != compactedTurnContinuation ||
		result.Request.Messages[2].ID != "latest-call" ||
		result.Request.Messages[3].ID != "latest-result" {
		t.Fatalf("projection = %#v", result.Request.Messages)
	}
	if !strings.Contains(model.request.Messages[0].Content, "first-call") || !strings.Contains(model.request.Messages[0].Content, "first-result") {
		t.Fatalf("active-turn prefix was not summarized: %q", model.request.Messages[0].Content)
	}

	transcript := append(storage.CloneMessages(request.Messages), compactionCheckpoint(
		"checkpoint",
		result.Checkpoint.CoversThroughMessageID,
		result.Checkpoint.TailStartMessageID,
		result.Checkpoint.Summary,
	))
	projected, err := ProjectCompactionCheckpoints(compactionRequest(transcript...))
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Messages) != 4 ||
		projected.Messages[1].Type != MessageTypeRuntimeEvent ||
		projected.Messages[2].ID != "latest-call" ||
		projected.Messages[3].ID != "latest-result" {
		t.Fatalf("resumed projection = %#v", projected.Messages)
	}
	if err := validateCompactionToolAdjacency(projected.Messages); err != nil {
		t.Fatalf("projected tool adjacency = %v", err)
	}
}

func TestCompactorDynamicTailKeepsMoreThanFormerQuarterBudget(t *testing.T) {
	oldUser := compactionMessage("old-user", MessageTypeUser, strings.Repeat("old ", 1150))
	oldAssistant := compactionMessage("old-assistant", MessageTypeAssistant, strings.Repeat("answer ", 575))
	currentUser := compactionMessage("current", MessageTypeUser, strings.Repeat("current ", 500))
	model := &compactionModel{content: "durable memory"}
	result, err := (Compactor{Model: model}).Prepare(context.Background(), CompactionInput{
		Request:           compactionRequest(oldUser, oldAssistant, currentUser),
		MainModelMetadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || len(result.Request.Messages) != 2 || result.Request.Messages[1].ID != "current" {
		t.Fatalf("result = %#v", result)
	}
	budgets := deriveCompactionBudgets(ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512}, 512)
	currentEstimate, err := (GenericContextEstimator{}).Estimate(compactionRequest(currentUser))
	if err != nil {
		t.Fatal(err)
	}
	if currentEstimate.Tokens <= budgets.input/4 {
		t.Fatalf("current tail estimate = %d; test requires more than former quarter budget %d", currentEstimate.Tokens, budgets.input/4)
	}
}

func TestCompactorDynamicTailAccountsForToolBaseCost(t *testing.T) {
	current := compactionMessage("current", MessageTypeUser, strings.Repeat("current ", 120))
	units := conversationUnits([]Message{current})
	withoutTools := selectRecentTail(ModelRequest{}, units, 1000, 100, GenericContextEstimator{})
	if len(withoutTools) != 1 {
		t.Fatalf("tail without tools = %#v", withoutTools)
	}
	withTools := selectRecentTail(ModelRequest{Tools: []ToolDefinition{{
		Name: "large-tool", Description: strings.Repeat("schema ", 600),
	}}}, units, 1000, 100, GenericContextEstimator{})
	if withTools != nil {
		t.Fatalf("tail with oversized tool base = %#v; want no available tail", withTools)
	}
}

func TestCompactorPlaceholderCountsAgainstDynamicAvailableBudget(t *testing.T) {
	oldUser := compactionMessage("old", MessageTypeUser, strings.Repeat("old ", 500))
	middleUser := compactionMessage("middle-user", MessageTypeUser, strings.Repeat("middle ", 43))
	middleAssistant := compactionMessage("middle-assistant", MessageTypeAssistant, strings.Repeat("answer ", 43))
	latestUser := compactionMessage("latest", MessageTypeUser, strings.Repeat("latest ", 29))
	model := &compactionModel{content: strings.Repeat("m", 1024)}
	result, err := (Compactor{Model: model}).Prepare(context.Background(), CompactionInput{Request: compactionRequest(oldUser, middleUser, middleAssistant, latestUser), MainModelMetadata: ModelMetadata{ContextWindowTokens: 500, MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || len(result.Request.Messages) != 2 || result.Request.Messages[1].ID != "latest" {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(model.request.Messages[0].Content, "middle-user") || !strings.Contains(model.request.Messages[0].Content, "middle-assistant") {
		t.Fatalf("oversized excluded tail was not summarized: %q", model.request.Messages[0].Content)
	}
}

func TestCompactorPrepareRejectsInvalidCompactionModelMetadata(t *testing.T) {
	message := compactionMessage("one", MessageTypeUser, strings.Repeat("x", 2000))
	model := &limitedCompactionModel{compactionModel: compactionModel{content: "memory"}, metadata: ModelMetadata{ContextWindowTokens: 10, MaxOutputTokens: 11}}
	_, err := (Compactor{Model: model}).Prepare(context.Background(), CompactionInput{Request: compactionRequest(message), MainModelMetadata: ModelMetadata{ContextWindowTokens: 500, MaxOutputTokens: 100}})
	if !errors.Is(err, ErrInvalidModelMetadata) {
		t.Fatalf("error = %v", err)
	}
}

func TestCompactorPreparePreselectsLegalTailBeforeSummary(t *testing.T) {
	oldUser := compactionMessage("old-user", MessageTypeUser, strings.Repeat("old ", 500))
	oldAssistant := compactionMessage("old-assistant", MessageTypeAssistant, "old answer")
	currentUser := compactionMessage("current-user", MessageTypeUser, "continue")
	currentAssistant := compactionMessage("current-assistant", MessageTypeAssistant, "in progress")
	model := &compactionModel{content: strings.Repeat("m", 1024)}
	result, err := (Compactor{Model: model}).Prepare(context.Background(), CompactionInput{Request: compactionRequest(oldUser, oldAssistant, currentUser, currentAssistant), MainModelMetadata: ModelMetadata{ContextWindowTokens: 500, MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || result.Request.Messages[1].Type != MessageTypeUser {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(model.request.Messages[0].Content, "old-user") || !strings.Contains(model.request.Messages[0].Content, "old answer") {
		t.Fatalf("summary omitted excluded history: %q", model.request.Messages[0].Content)
	}
}

func TestCompactorRejectsInvalidToolTranscriptAndProjection(t *testing.T) {
	call := compactionMessage("call", MessageTypeToolCall, "")
	call.ToolCalls = []ToolCall{{CallID: "one", Name: "first", Arguments: []byte(`{}`)}, {CallID: "two", Name: "second", Arguments: []byte(`{}`)}}
	validResult := compactionMessage("result", MessageTypeToolResult, "")
	validResult.ToolResult = &ToolResult{CallID: "one", Name: "first", Status: ToolResultSucceeded, Output: []byte(`{}`)}
	tests := []struct {
		name     string
		messages []Message
	}{
		{name: "orphan", messages: []Message{toolResultMessage("orphan", "none", "tool")}},
		{name: "mismatched name", messages: []Message{call, toolResultMessage("bad", "one", "wrong"), toolResultMessage("two", "two", "second")}},
		{name: "partial multi call", messages: []Message{call, validResult}},
		{name: "interleaved", messages: []Message{call, compactionMessage("user", MessageTypeUser, "interrupt")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := compactionRequest(test.messages...)
			if _, err := (Compactor{}).Prepare(context.Background(), CompactionInput{Request: request, MainModelMetadata: ModelMetadata{ContextWindowTokens: 1000, MaxOutputTokens: 100}}); !errors.Is(err, ErrInvalidCompactionToolAdjacency) {
				t.Fatalf("Prepare error = %v", err)
			}
			if _, err := ProjectCompactionCheckpoints(request); !errors.Is(err, ErrInvalidCompactionToolAdjacency) {
				t.Fatalf("Project error = %v", err)
			}
		})
	}
}

func TestCompactorPrepareFailsForEmptySummaryAndOversizedProjection(t *testing.T) {
	message := compactionMessage("one", MessageTypeUser, strings.Repeat("x", 2000))
	_, err := (Compactor{Model: &compactionModel{content: ""}}).Prepare(context.Background(), CompactionInput{Request: compactionRequest(message), MainModelMetadata: ModelMetadata{ContextWindowTokens: 500, MaxOutputTokens: 100}})
	if err == nil || (!strings.Contains(err.Error(), "empty summary") && !errors.Is(err, ErrCompactionStillTooLarge)) {
		t.Fatalf("error = %v", err)
	}
}

func compactionRequest(messages ...Message) ModelRequest {
	return ModelRequest{SessionID: "session", TurnID: "turn", Messages: messages}
}
func compactionMessage(id string, kind MessageType, content string) Message {
	return Message{ID: id, SessionID: "session", TurnID: "turn", Type: kind, Content: content, CreatedAt: time.Unix(1, 0)}
}
func compactionCheckpoint(id, cover, tail, summary string) Message {
	return Message{ID: id, SessionID: "session", TurnID: "turn", Type: storage.MessageTypeCompactionCheckpoint, CompactionCheckpoint: &storage.CompactionCheckpoint{Summary: summary, CoversThroughMessageID: cover, TailStartMessageID: tail}, CreatedAt: time.Unix(1, 0)}
}
func toolResultMessage(id, callID, name string) Message {
	message := compactionMessage(id, MessageTypeToolResult, "")
	message.ToolResult = &ToolResult{CallID: callID, Name: name, Status: ToolResultSucceeded, Output: []byte(`{}`)}
	return message
}

type compactionModel struct {
	request  ModelRequest
	requests []ModelRequest
	content  string
}

type limitedCompactionModel struct {
	compactionModel
	metadata ModelMetadata
}

func (m *limitedCompactionModel) ModelMetadata() (ModelMetadata, error) { return m.metadata, nil }

func (m *compactionModel) Start(_ context.Context, request ModelRequest) (ModelStream, error) {
	m.request = request.Clone()
	m.requests = append(m.requests, m.request)
	return compactionStream{content: m.content}, nil
}

type compactionStream struct{ content string }

func (s compactionStream) Subscribe(context.Context) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.ContentReceived, Content: s.content}
	ch <- provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: s.content, Finished: true}}}
	close(ch)
	return ch
}
func (s compactionStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{Content: s.content, Finished: true}, nil
}
