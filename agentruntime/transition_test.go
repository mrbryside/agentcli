package agentruntime

import (
	"reflect"
	"testing"

	"github.com/mrbryside/agentcli/provider"
)

func TestEffectsLifecycleOrder(t *testing.T) {
	user := Message{ID: "msg_user", SessionID: "session", TurnID: "turn", Type: MessageTypeUser, Content: "hello"}
	completed := provider.StreamEvent{
		Type:    provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "done", Finished: true}},
	}
	toolCompleted := provider.StreamEvent{
		Type: provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "call_1", Name: "weather", Arguments: map[string]any{"city": "Bangkok"},
		}}}},
	}

	tests := []struct {
		name  string
		state AgentState
		event AgentEvent
		want  []EffectType
	}{
		{
			name:  "run started",
			event: AgentEvent{SessionID: "session", TurnID: "turn", Type: RunStarted, Message: &user},
			want:  []EffectType{AppendMessages, StartProvider},
		},
		{
			name:  "non terminal provider event",
			event: AgentEvent{SessionID: "session", TurnID: "turn", Type: ProviderEventReceived, ProviderEvent: provider.StreamEvent{Type: provider.ContentReceived, Content: "partial"}},
		},
		{
			name:  "final provider completion",
			event: AgentEvent{SessionID: "session", TurnID: "turn", Type: ProviderEventReceived, ProviderEvent: completed},
			want:  []EffectType{AppendMessages, AttemptComplete},
		},
		{
			name:  "provider tool completion",
			event: AgentEvent{SessionID: "session", TurnID: "turn", Type: ProviderEventReceived, ProviderEvent: toolCompleted},
			want:  []EffectType{AppendMessages, EmitEvent},
		},
		{
			name:  "tool request",
			event: AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolCallRequested, ToolRequest: &ToolRequest{SessionID: "session", TurnID: "turn", Call: ToolCall{CallID: "call_1", Name: "weather", Arguments: []byte(`{}`)}}},
			want:  []EffectType{DispatchTool},
		},
		{
			name:  "run completed",
			event: AgentEvent{SessionID: "session", TurnID: "turn", Type: RunCompleted},
			want:  []EffectType{CompleteRun, CloseRun},
		},
		{
			name:  "max step failure",
			event: AgentEvent{SessionID: "session", TurnID: "turn", Type: RunFailed, Error: ErrMaxSteps},
			want:  []EffectType{FailRun, CloseRun},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			effects, err := Effects(test.state, test.event)
			if err != nil {
				t.Fatalf("Effects() error = %v", err)
			}
			if got := effectTypes(effects); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("effect types = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProviderCompletionPersistsReasoningSeparately(t *testing.T) {
	event := AgentEvent{
		SessionID: "session",
		TurnID:    "turn",
		Type:      ProviderEventReceived,
		ProviderEvent: provider.StreamEvent{
			Type: provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
				Content: "answer", Reasoning: "internal reasoning", Finished: true,
			}},
		},
	}

	effects, err := Effects(AgentState{}, event)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) == 0 || len(effects[0].Messages) != 1 {
		t.Fatalf("effects = %#v, want one stored assistant message", effects)
	}
	message := effects[0].Messages[0]
	if message.Content != "answer" || message.Reasoning != "internal reasoning" {
		t.Fatalf("stored assistant = %#v", message)
	}
}

func TestEffectsWaitsForEveryToolResultAndPersistsProviderOrder(t *testing.T) {
	state := toolRoundState(t)
	second := ToolResultEnvelope{SessionID: "session", TurnID: "turn", Result: ToolResult{CallID: "call_2", Name: "second", Status: ToolResultSucceeded, Output: []byte(`2`)}}
	first := ToolResultEnvelope{SessionID: "session", TurnID: "turn", Result: ToolResult{CallID: "call_1", Name: "first", Status: ToolResultSucceeded, Output: []byte(`1`)}}

	firstEffects, err := Effects(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &second})
	if err != nil {
		t.Fatalf("Effects(second result) error = %v", err)
	}
	if len(firstEffects) != 0 {
		t.Fatalf("Effects(second result) = %v, want no effects while waiting", effectTypes(firstEffects))
	}

	state = State(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &second})
	effects, err := Effects(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &first})
	if err != nil {
		t.Fatalf("Effects(first result) error = %v", err)
	}
	if got, want := effectTypes(effects), []EffectType{AppendMessages, StartProvider}; !reflect.DeepEqual(got, want) {
		t.Fatalf("effect types = %v, want %v", got, want)
	}
	if got := effects[0].Messages; len(got) != 2 || got[0].ToolResult.CallID != "call_1" || got[1].ToolResult.CallID != "call_2" {
		t.Fatalf("ordered result messages = %#v, want call_1 then call_2", got)
	}
}

func TestEffectsEndsTurnAfterSuccessfulConfiguredToolBatch(t *testing.T) {
	state := toolRoundState(t)
	first := ToolResultEnvelope{
		SessionID: "session", TurnID: "turn", TurnBehavior: ToolTurnEnd,
		Result: ToolResult{CallID: "call_1", Name: "first", Status: ToolResultSucceeded, Output: []byte(`1`)},
	}
	second := ToolResultEnvelope{
		SessionID: "session", TurnID: "turn", TurnBehavior: ToolTurnEnd,
		Result: ToolResult{CallID: "call_2", Name: "second", Status: ToolResultSucceeded, Output: []byte(`2`)},
	}

	state = State(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &first})
	effects, err := Effects(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &second})
	if err != nil {
		t.Fatalf("Effects() error = %v", err)
	}
	if got, want := effectTypes(effects), []EffectType{AppendMessages, AttemptComplete}; !reflect.DeepEqual(got, want) {
		t.Fatalf("effect types = %v, want %v", got, want)
	}
}

func TestEffectsContinuesWhenSuccessfulBatchIncludesContinueTurn(t *testing.T) {
	state := toolRoundState(t)
	first := ToolResultEnvelope{
		SessionID: "session", TurnID: "turn", TurnBehavior: ToolTurnEnd,
		Result: ToolResult{CallID: "call_1", Name: "first", Status: ToolResultSucceeded, Output: []byte(`1`)},
	}
	second := ToolResultEnvelope{
		SessionID: "session", TurnID: "turn", TurnBehavior: ToolTurnContinue,
		Result: ToolResult{CallID: "call_2", Name: "second", Status: ToolResultSucceeded, Output: []byte(`2`)},
	}

	state = State(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &first})
	effects, err := Effects(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &second})
	if err != nil {
		t.Fatalf("Effects() error = %v", err)
	}
	if got, want := effectTypes(effects), []EffectType{AppendMessages, StartProvider}; !reflect.DeepEqual(got, want) {
		t.Fatalf("effect types = %v, want %v", got, want)
	}
}

func TestEffectsContinuesWhenEndTurnBatchContainsFailure(t *testing.T) {
	state := toolRoundState(t)
	first := ToolResultEnvelope{
		SessionID: "session", TurnID: "turn", TurnBehavior: ToolTurnEnd,
		Result: ToolResult{CallID: "call_1", Name: "first", Status: ToolResultSucceeded, Output: []byte(`1`)},
	}
	second := ToolResultEnvelope{
		SessionID: "session", TurnID: "turn",
		Result: ToolResult{CallID: "call_2", Name: "second", Status: ToolResultFailed, Error: "failed"},
	}

	state = State(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &first})
	effects, err := Effects(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &second})
	if err != nil {
		t.Fatalf("Effects() error = %v", err)
	}
	if got, want := effectTypes(effects), []EffectType{AppendMessages, StartProvider}; !reflect.DeepEqual(got, want) {
		t.Fatalf("effect types = %v, want %v", got, want)
	}
}

func TestEffectsRejectsDuplicateAndUnknownToolResults(t *testing.T) {
	state := toolRoundState(t)
	accepted := ToolResultEnvelope{SessionID: "session", TurnID: "turn", Result: ToolResult{CallID: "call_1", Name: "first", Status: ToolResultSucceeded, Output: []byte(`1`)}}
	state = State(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &accepted})

	duplicate, err := Effects(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &accepted})
	if err != nil {
		t.Fatalf("Effects(duplicate) error = %v", err)
	}
	if len(duplicate) != 0 {
		t.Fatalf("Effects(duplicate) = %v, want no effects", effectTypes(duplicate))
	}

	unknown := ToolResultEnvelope{SessionID: "session", TurnID: "turn", Result: ToolResult{CallID: "unknown", Name: "other", Status: ToolResultSucceeded, Output: []byte(`null`)}}
	effects, err := Effects(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &unknown})
	if err != nil {
		t.Fatalf("Effects(unknown) error = %v", err)
	}
	if got, want := effectTypes(effects), []EffectType{EmitEvent}; !reflect.DeepEqual(got, want) {
		t.Fatalf("effect types = %v, want %v", got, want)
	}
	if effects[0].Event == nil || effects[0].Event.Type != RunFailed || effects[0].Event.Error == nil {
		t.Fatalf("unknown result effect = %#v, want RunFailed emission", effects[0])
	}
}

func TestEffectsFailsInvalidProviderToolMetadata(t *testing.T) {
	completion := provider.StreamEvent{
		Type: provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{CompletedTools: []provider.ToolCall{{
			ID: "", Name: "weather", Arguments: map[string]any{},
		}}}},
	}
	effects, err := Effects(EmptyState(), AgentEvent{SessionID: "session", TurnID: "turn", Type: ProviderEventReceived, ProviderEvent: completion})
	if err != nil {
		t.Fatalf("Effects() error = %v", err)
	}
	if got, want := effectTypes(effects), []EffectType{EmitEvent}; !reflect.DeepEqual(got, want) {
		t.Fatalf("effect types = %v, want %v", got, want)
	}
	if effects[0].Event == nil || effects[0].Event.Type != RunFailed || effects[0].Event.Error == nil {
		t.Fatalf("invalid provider tool effect = %#v, want RunFailed emission", effects[0])
	}
}

func TestEffectsInterruptsPendingToolsInOrder(t *testing.T) {
	state := toolRoundState(t)
	completed := ToolResultEnvelope{SessionID: "session", TurnID: "turn", Result: ToolResult{CallID: "call_1", Name: "first", Status: ToolResultSucceeded, Output: []byte(`1`)}}
	state = State(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolResultReceived, ToolResult: &completed})

	effects, err := Effects(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: AgentInterrupted, Reason: "caller cancelled"})
	if err != nil {
		t.Fatalf("Effects() error = %v", err)
	}
	if got, want := effectTypes(effects), []EffectType{InterruptTools, CancelProvider, AppendMessages, CloseRun}; !reflect.DeepEqual(got, want) {
		t.Fatalf("effect types = %v, want %v", got, want)
	}
	if got := effects[0].ToolInterrupt; got == nil || !reflect.DeepEqual(got.CallIDs, []string{"call_2"}) || got.Reason != "caller cancelled" {
		t.Fatalf("interrupt = %#v, want pending call_2", got)
	}
	if got := effects[2].Messages; len(got) != 2 || got[0].ToolResult.CallID != "call_1" || got[0].ToolResult.Status != ToolResultSucceeded || got[1].ToolResult.CallID != "call_2" || got[1].ToolResult.Status != ToolResultInterrupted || got[1].ToolResult.Error != "caller cancelled" {
		t.Fatalf("synthetic results = %#v", got)
	}
}

func toolRoundState(t *testing.T) AgentState {
	t.Helper()
	result := provider.StreamResult{CompletedTools: []provider.ToolCall{
		{ID: "call_1", Name: "first", Arguments: map[string]any{}},
		{ID: "call_2", Name: "second", Arguments: map[string]any{}},
	}}
	completion := AgentEvent{SessionID: "session", TurnID: "turn", Type: ProviderEventReceived, ProviderEvent: provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: result}}}
	state := State(EmptyState(), completion)
	for _, call := range result.CompletedTools {
		arguments := []byte(`{}`)
		state = State(state, AgentEvent{SessionID: "session", TurnID: "turn", Type: ToolCallRequested, ToolRequest: &ToolRequest{SessionID: "session", TurnID: "turn", Call: ToolCall{CallID: call.ID, Name: call.Name, Arguments: arguments}}})
	}
	return state
}

func effectTypes(effects []Effect) []EffectType {
	if effects == nil {
		return nil
	}
	types := make([]EffectType, len(effects))
	for index, effect := range effects {
		types[index] = effect.Type
	}
	return types
}
