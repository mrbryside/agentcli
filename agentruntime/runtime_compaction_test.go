package agentruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

type runtimeCompactionModel struct {
	mu          sync.Mutex
	metadata    ModelMetadata
	metadataErr error
	requests    []ModelRequest
	streams     []ModelStream
	startErr    error
	startErrors []error
}

type runtimeEstimatorModel struct {
	*runtimeCompactionModel
	estimator ContextEstimator
}

func (m *runtimeEstimatorModel) ContextEstimator() ContextEstimator {
	return m.estimator
}

func (m *runtimeCompactionModel) ModelMetadata() (ModelMetadata, error) {
	return m.metadata, m.metadataErr
}

func (m *runtimeCompactionModel) Start(_ context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request.Clone())
	if len(m.startErrors) != 0 {
		err := m.startErrors[0]
		m.startErrors = m.startErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	if m.startErr != nil {
		return nil, m.startErr
	}
	if len(m.streams) == 0 {
		return nil, nil
	}
	stream := m.streams[0]
	m.streams = m.streams[1:]
	return stream, nil
}

func TestRuntimeCompactionRequiresMainModelMetadata(t *testing.T) {
	_, err := New(context.Background(), Config{
		Model: runtimeModel{}, Messages: inmemory.NewMessageStorage(), Compactor: &Compactor{Model: &runtimeCompactionModel{}},
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
	})
	if err == nil || !strings.Contains(err.Error(), "main model metadata") {
		t.Fatalf("New() error = %v, want main model metadata error", err)
	}
}

func TestRuntimeCompactionRejectsInvalidMainModelMetadata(t *testing.T) {
	_, err := New(context.Background(), Config{
		Model: &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 10, MaxOutputTokens: 11}}, Messages: inmemory.NewMessageStorage(), Compactor: &Compactor{Model: &runtimeCompactionModel{}},
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
	})
	if !errors.Is(err, ErrInvalidModelMetadata) {
		t.Fatalf("New() error = %v, want ErrInvalidModelMetadata", err)
	}
}

func TestRuntimeCompactionRejectsCompactionModelMetadataAtStartup(t *testing.T) {
	base := Config{
		Model: &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 100, MaxOutputTokens: 20}}, Messages: inmemory.NewMessageStorage(),
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
	}
	for _, test := range []struct {
		name     string
		model    *runtimeCompactionModel
		want     error
		wantText string
	}{
		{name: "metadata error", model: &runtimeCompactionModel{metadataErr: errors.New("unknown model")}, wantText: "unknown model"},
		{name: "invalid metadata", model: &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 10, MaxOutputTokens: 11}}, want: ErrInvalidModelMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := base
			config.Compactor = &Compactor{Model: test.model}
			_, err := New(context.Background(), config)
			if (test.want != nil && !errors.Is(err, test.want)) || (test.wantText != "" && !strings.Contains(err.Error(), test.wantText)) {
				t.Fatalf("New() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRuntimeCompactionAppliesOperationalOutputCapToMainProvider(t *testing.T) {
	metadata := ModelMetadata{ContextWindowTokens: 122880, MaxOutputTokens: 66560}
	main := &runtimeCompactionModel{metadata: metadata, streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{
		Type: provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
			Content: "done", Finished: true,
		}},
	}}}}}
	summarizer := &runtimeCompactionModel{metadata: metadata}
	runtime, err := New(context.Background(), Config{
		Model: main, Messages: inmemory.NewMessageStorage(), Compactor: &Compactor{Model: summarizer},
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRuntimeEvents(t, run)
	wantOutput := operationalMaxOutputTokens(0, metadata)
	if len(main.requests) != 1 || main.requests[0].MaxOutputTokens != wantOutput {
		t.Fatalf("main requests = %#v", main.requests)
	}
	if len(summarizer.requests) != 0 {
		t.Fatalf("unexpected compaction = %#v", summarizer.requests)
	}
}

func TestRuntimeCompactionUsesMainModelContextEstimator(t *testing.T) {
	called := false
	estimator := ContextEstimatorFunc(func(ModelRequest) (ContextEstimate, error) {
		called = true
		return ContextEstimate{Tokens: 7}, nil
	})
	main := &runtimeEstimatorModel{
		runtimeCompactionModel: &runtimeCompactionModel{
			metadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512},
		},
		estimator: estimator,
	}
	summarizer := &runtimeCompactionModel{
		metadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512},
	}
	runtime, err := New(context.Background(), Config{
		Model: main, Messages: inmemory.NewMessageStorage(), Compactor: &Compactor{Model: summarizer},
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.compactor.Estimator == nil {
		t.Fatal("runtime did not select the main model estimator")
	}
	if _, err := runtime.compactor.Estimator.Estimate(ModelRequest{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("main model context estimator was not used")
	}
}

func TestRuntimeForceCompactsAndRetriesOnceAfterContextWindowRejection(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	if err := messages.Append(context.Background(), storage.Message{
		ID: "old", SessionID: "session", TurnID: "old-turn", Type: storage.MessageTypeUser, Content: "older context",
	}); err != nil {
		t.Fatal(err)
	}
	main := &runtimeEstimatorModel{
		runtimeCompactionModel: &runtimeCompactionModel{
			metadata:    ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512},
			startErrors: []error{ErrContextWindowExceeded},
			streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{
				Type: provider.StreamCompleted,
				Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
					Content: "done", Finished: true,
				}},
			}}}},
		},
		estimator: ContextEstimatorFunc(func(ModelRequest) (ContextEstimate, error) {
			return ContextEstimate{Tokens: 1}, nil
		}),
	}
	summarizer := &runtimeCompactionModel{
		metadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512},
		streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{
			Type: provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
				Content: "# Objective\nforced retry", Finished: true,
			}},
		}}}},
	}
	runtime, err := New(context.Background(), Config{
		Model: main, Messages: messages, Compactor: &Compactor{Model: summarizer},
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "new request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectRuntimeEvents(t, run)
	if countEvent(events, CompactionStarted) != 1 || countEvent(events, CompactionCompleted) != 1 || countEvent(events, CompactionFailed) != 0 {
		t.Fatalf("unexpected forced compaction events: %#v", events)
	}
	if len(main.requests) != 2 {
		t.Fatalf("main requests = %d; want rejected request plus one retry", len(main.requests))
	}
	if len(summarizer.requests) != 1 {
		t.Fatalf("summarizer requests = %d; want 1", len(summarizer.requests))
	}
	if len(main.requests[1].Messages) == 0 || main.requests[1].Messages[0].Type != MessageTypeAssistant {
		t.Fatalf("retry request was not compacted: %#v", main.requests[1])
	}
}

func TestRuntimeForceCompactionRetriesProviderOnlyOnce(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	if err := messages.Append(context.Background(), storage.Message{
		ID: "old", SessionID: "session", TurnID: "old-turn", Type: storage.MessageTypeUser, Content: "older context",
	}); err != nil {
		t.Fatal(err)
	}
	main := &runtimeEstimatorModel{
		runtimeCompactionModel: &runtimeCompactionModel{
			metadata:    ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512},
			startErrors: []error{ErrContextWindowExceeded, ErrContextWindowExceeded},
		},
		estimator: ContextEstimatorFunc(func(ModelRequest) (ContextEstimate, error) {
			return ContextEstimate{Tokens: 1}, nil
		}),
	}
	summarizer := &runtimeCompactionModel{
		metadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512},
		streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{
			Type: provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
				Content: "# Objective\nforced retry", Finished: true,
			}},
		}}}},
	}
	runtime, err := New(context.Background(), Config{
		Model: main, Messages: messages, Compactor: &Compactor{Model: summarizer},
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "new request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectRuntimeEvents(t, run)
	if countEvent(events, RunFailed) != 1 {
		t.Fatalf("events = %#v; want one terminal failure", events)
	}
	if len(main.requests) != 2 {
		t.Fatalf("main requests = %d; want initial request plus exactly one retry", len(main.requests))
	}
	if len(summarizer.requests) != 1 {
		t.Fatalf("summarizer requests = %d; want exactly one forced compaction", len(summarizer.requests))
	}
}

func TestRuntimeCompactionFailurePreventsMainProviderStart(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	if err := messages.Append(context.Background(), storage.Message{ID: "old", SessionID: "session", TurnID: "old-turn", Type: storage.MessageTypeUser, Content: strings.Repeat("old ", 400)}); err != nil {
		t.Fatal(err)
	}
	main := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 500, MaxOutputTokens: 100}}
	summarizer := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512}, startErr: errors.New("summarizer unavailable")}
	runtime, err := New(context.Background(), Config{Model: main, Messages: messages, Compactor: &Compactor{Model: summarizer}, ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	events := collectRuntimeEvents(t, run)
	if countEvent(events, CompactionStarted) != 1 || countEvent(events, CompactionFailed) != 1 || countEvent(events, CompactionCompleted) != 0 {
		t.Fatalf("unexpected compaction failure events: %#v", events)
	}
	if len(main.requests) != 0 {
		t.Fatalf("main model started after compaction failure: %#v", main.requests)
	}
}

func TestRuntimeCompactionPersistsCheckpointAndProjectsMainRequest(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	old := storage.Message{ID: "old", SessionID: "session", TurnID: "old-turn", Type: storage.MessageTypeUser, Content: strings.Repeat("old ", 400)}
	if err := messages.Append(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	main := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 500, MaxOutputTokens: 100}, streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}}}}}}}
	summarizer := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512}, streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "# Objective\nwork", Finished: true}}}}}}}
	runtime, err := New(context.Background(), Config{Model: main, Messages: messages, Compactor: &Compactor{Model: summarizer}, ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	events := collectRuntimeEvents(t, run)
	if countEvent(events, CompactionStarted) != 1 || countEvent(events, CompactionCompleted) != 1 || countEvent(events, CompactionFailed) != 0 {
		t.Fatalf("unexpected compaction events: %#v", events)
	}
	if len(main.requests) != 1 || len(main.requests[0].Messages) == 0 || main.requests[0].Messages[0].Type != MessageTypeAssistant {
		t.Fatalf("main request was not projected: %#v", main.requests)
	}
	for _, message := range main.requests[0].Messages {
		if message.Type == storage.MessageTypeCompactionCheckpoint || message.ID == "old" {
			t.Fatalf("raw compacted history reached main model: %#v", main.requests[0].Messages)
		}
	}
	stored, err := messages.List(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	checkpointFound := false
	for _, message := range stored {
		checkpointFound = checkpointFound || message.Type == storage.MessageTypeCompactionCheckpoint
	}
	if !checkpointFound {
		t.Fatalf("stored messages contain no checkpoint: %#v", stored)
	}
	started, completed, providerEvent := -1, -1, -1
	for index, event := range events {
		switch event.Type {
		case CompactionStarted:
			started = index
		case CompactionCompleted:
			completed = index
		case ProviderEventReceived:
			if providerEvent < 0 {
				providerEvent = index
			}
		}
	}
	if !(started >= 0 && started < completed && completed < providerEvent) {
		t.Fatalf("compaction/provider event order = %#v", events)
	}
}

func TestRuntimeRepeatedCompactionMergesCheckpointAndProjectsResume(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	if err := messages.Append(context.Background(), storage.Message{ID: "old", SessionID: "session", TurnID: "old", Type: storage.MessageTypeUser, Content: strings.Repeat("old ", 450)}); err != nil {
		t.Fatal(err)
	}
	main := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 600, MaxOutputTokens: 100}, streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "first answer", Finished: true}}}}},
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "second answer", Finished: true}}}}},
	}}
	summarizer := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512}, streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "# Objective\nfirst memory", Finished: true}}}}},
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "# Objective\nsecond memory", Finished: true}}}}},
	}}
	runtime, err := New(context.Background(), Config{Model: main, Messages: messages, Compactor: &Compactor{Model: summarizer}, ToolRequests: make(chan ToolRequest, 2), ToolResults: make(chan ToolResultEnvelope, 2), ToolInterrupts: make(chan ToolInterrupt, 2)})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []string{"one", "two"} {
		run, startErr := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: turn, Message: Message{Type: MessageTypeUser, Content: strings.Repeat("new ", 180)}})
		if startErr != nil {
			t.Fatal(startErr)
		}
		collectRuntimeEvents(t, run)
	}
	if len(summarizer.requests) != 2 || !strings.Contains(summarizer.requests[1].Messages[0].Content, "first memory") {
		t.Fatalf("second summarizer did not receive cumulative memory: %#v", summarizer.requests)
	}
	if len(main.requests) != 2 {
		t.Fatalf("main requests = %d, want 2", len(main.requests))
	}
	for _, request := range main.requests {
		for _, message := range request.Messages {
			if message.ID == "old" || message.Type == storage.MessageTypeCompactionCheckpoint {
				t.Fatalf("resumed request exposed covered transcript: %#v", request.Messages)
			}
		}
	}
}

func TestRuntimeCompactionSeparatesConcurrentSessions(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	for _, session := range []string{"a", "b"} {
		if err := messages.Append(context.Background(), storage.Message{ID: "old-" + session, SessionID: session, TurnID: "old", Type: storage.MessageTypeUser, Content: strings.Repeat(session+" ", 800)}); err != nil {
			t.Fatal(err)
		}
	}
	main := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 500, MaxOutputTokens: 100}, streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}}}}},
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}}}}},
	}}
	summarizer := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512}, streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "memory a", Finished: true}}}}},
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "memory b", Finished: true}}}}},
	}}
	runtime, err := New(context.Background(), Config{Model: main, Messages: messages, Compactor: &Compactor{Model: summarizer}, ToolRequests: make(chan ToolRequest, 2), ToolResults: make(chan ToolResultEnvelope, 2), ToolInterrupts: make(chan ToolInterrupt, 2)})
	if err != nil {
		t.Fatal(err)
	}
	runs := make(chan *Run, 2)
	for _, session := range []string{"a", "b"} {
		go func(session string) {
			run, _ := runtime.Start(context.Background(), Request{SessionID: session, TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "new"}})
			runs <- run
		}(session)
	}
	for range 2 {
		if run := <-runs; run == nil {
			t.Fatal("concurrent Start failed")
		} else {
			collectRuntimeEvents(t, run)
		}
	}
	if len(main.requests) != 2 {
		t.Fatalf("main requests = %#v", main.requests)
	}
	for _, request := range main.requests {
		other := "old-a"
		if request.SessionID == "a" {
			other = "old-b"
		}
		for _, message := range request.Messages {
			if message.ID == other {
				t.Fatalf("cross-session request: %#v", request)
			}
		}
	}
}

func TestRuntimeCompactionPreservesToolRoundRequestBoundaries(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	if err := messages.Append(context.Background(), storage.Message{ID: "old", SessionID: "session", TurnID: "old", Type: storage.MessageTypeUser, Content: strings.Repeat("old ", 700)}); err != nil {
		t.Fatal(err)
	}
	main := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 800, MaxOutputTokens: 100}, streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{{ID: "call", Name: "tool", Arguments: map[string]any{}}}, Finished: true}}}}},
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}}}}},
	}}
	summarizer := &runtimeCompactionModel{metadata: ModelMetadata{ContextWindowTokens: 4096, MaxOutputTokens: 512}, streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "memory", Finished: true}}}}},
	}}
	requests := make(chan ToolRequest, 1)
	results := make(chan ToolResultEnvelope, 1)
	runtime, err := New(context.Background(), Config{Model: main, Messages: messages, Compactor: &Compactor{Model: summarizer}, Tools: []ToolDefinition{{Name: "tool"}}, ToolRequests: requests, ToolResults: results, ToolInterrupts: make(chan ToolInterrupt, 1)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: strings.Repeat("new ", 180)}})
	if err != nil {
		t.Fatal(err)
	}
	tool := receiveToolRequest(t, requests)
	results <- successfulEnvelope(tool.SessionID, tool.TurnID, tool.Call.CallID, tool.Call.Name, `null`)
	collectRuntimeEvents(t, run)
	if len(main.requests) != 2 {
		t.Fatalf("main rounds = %d, want tool continuation", len(main.requests))
	}
	for _, request := range main.requests {
		if err := validateCompactionToolAdjacency(request.Messages); err != nil {
			t.Fatalf("provider request split a tool round: %v; %#v", err, request.Messages)
		}
	}
}

func TestRuntimeProjectsCheckpointWhenAutoCompactionDisabled(t *testing.T) {
	messages := inmemory.NewMessageStorage()
	seed := []storage.Message{
		{ID: "old", SessionID: "session", TurnID: "old-turn", Type: storage.MessageTypeUser, Content: "old"},
		{ID: "tail", SessionID: "session", TurnID: "old-turn", Type: storage.MessageTypeUser, Content: "tail"},
		{ID: "checkpoint", SessionID: "session", TurnID: "old-turn", Type: storage.MessageTypeCompactionCheckpoint, CompactionCheckpoint: &storage.CompactionCheckpoint{Summary: "memory", CoversThroughMessageID: "old", TailStartMessageID: "tail"}},
	}
	if err := messages.Append(context.Background(), seed...); err != nil {
		t.Fatal(err)
	}
	main := &runtimeCompactionModel{streams: []ModelStream{scriptedStream{events: []provider.StreamEvent{{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}}}}}}}
	runtime, err := New(context.Background(), Config{Model: main, Messages: messages, ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{SessionID: "session", TurnID: "turn", Message: Message{Type: MessageTypeUser, Content: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	collectRuntimeEvents(t, run)
	if len(main.requests) != 1 || len(main.requests[0].Messages) < 3 {
		t.Fatalf("unexpected projected request: %#v", main.requests)
	}
	if main.requests[0].Messages[0].Type != MessageTypeAssistant || main.requests[0].Messages[1].ID != "tail" {
		t.Fatalf("checkpoint projection = %#v", main.requests[0].Messages)
	}
}
