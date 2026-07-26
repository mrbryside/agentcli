package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"
	provideropenai "github.com/mrbryside/agentcli/provider/openai"

	sdkopenai "github.com/sashabaranov/go-openai"
)

func TestAdapterModelIdentity(t *testing.T) {
	adapter := New(&fakeProvider{}, Config{Provider: "primary", Model: "gpt-test"})
	if got := adapter.ModelIdentity(); got != (agentruntime.ModelIdentity{Provider: "primary", Model: "gpt-test"}) {
		t.Fatalf("ModelIdentity() = %#v", got)
	}
	defaulted := New(&fakeProvider{}, Config{Model: "gpt-test"})
	if got := defaulted.ModelIdentity().Provider; got != "openai" {
		t.Fatalf("default provider identity = %q", got)
	}
}

func TestAdapterModelMetadata(t *testing.T) {
	t.Run("known aliases", func(t *testing.T) {
		for _, test := range []struct {
			model string
			want  agentruntime.ModelMetadata
		}{
			{model: "gpt-4.1-mini-2025-04-14", want: agentruntime.ModelMetadata{ContextWindowTokens: 1_047_576, MaxOutputTokens: 32_768}},
			{model: "gpt-4o", want: agentruntime.ModelMetadata{ContextWindowTokens: 128_000, MaxOutputTokens: 16_384}},
		} {
			t.Run(test.model, func(t *testing.T) {
				adapter := New(&fakeProvider{}, Config{Model: test.model})
				metadata, err := adapter.ModelMetadata()
				if err != nil {
					t.Fatalf("ModelMetadata: %v", err)
				}
				if metadata != test.want {
					t.Fatalf("metadata = %#v, want %#v", metadata, test.want)
				}
			})
		}
	})

	t.Run("unknown model is explicit", func(t *testing.T) {
		adapter := New(&fakeProvider{}, Config{Model: "compatible-model"})
		if _, err := adapter.ModelMetadata(); err == nil || !strings.Contains(err.Error(), "limits are unknown") || !strings.Contains(err.Error(), "Config.MetadataResolver") {
			t.Fatalf("ModelMetadata() error = %v, want explicit unknown-limits error", err)
		}
		// Unknown OpenAI-compatible models remain usable unless a runtime feature
		// actually requests their metadata.
		if _, err := adapter.Start(context.Background(), agentruntime.ModelRequest{}); err != nil {
			t.Fatalf("Start: %v", err)
		}
	})

	t.Run("custom resolver", func(t *testing.T) {
		called := false
		resolverErr := errors.New("unexpected")
		adapter := New(&fakeProvider{}, Config{
			Model: "compatible-model",
			MetadataResolver: func(model string) (agentruntime.ModelMetadata, error) {
				called = true
				if model != "compatible-model" {
					return agentruntime.ModelMetadata{}, resolverErr
				}
				return agentruntime.ModelMetadata{ContextWindowTokens: 32_768, MaxOutputTokens: 4_096}, nil
			},
		})
		metadata, err := adapter.ModelMetadata()
		if err != nil {
			t.Fatalf("ModelMetadata: %v", err)
		}
		if !called || metadata.ContextWindowTokens != 32_768 || metadata.MaxOutputTokens != 4_096 {
			t.Fatalf("resolver result = %#v, called = %t", metadata, called)
		}
	})

	t.Run("invalid custom metadata", func(t *testing.T) {
		adapter := New(&fakeProvider{}, Config{
			Model: "compatible-model",
			MetadataResolver: func(string) (agentruntime.ModelMetadata, error) {
				return agentruntime.ModelMetadata{ContextWindowTokens: 1, MaxOutputTokens: 2}, nil
			},
		})
		if _, err := adapter.ModelMetadata(); !errors.Is(err, agentruntime.ErrInvalidModelMetadata) {
			t.Fatalf("ModelMetadata() error = %v, want ErrInvalidModelMetadata", err)
		}
	})
}

func TestAdapterModelMetadataRejectsLookalikes(t *testing.T) {
	for _, model := range []string{
		"gpt-4-foo",
		"gpt-4.1-evil",
		"gpt-4.1-mini-unknown",
		"gpt-4o-compatible",
		"gpt-4.1-2025-13-99",
	} {
		t.Run(model, func(t *testing.T) {
			adapter := New(&fakeProvider{}, Config{Model: model})
			if _, err := adapter.ModelMetadata(); err == nil || !strings.Contains(err.Error(), "limits are unknown") {
				t.Fatalf("ModelMetadata() error = %v, want unknown-limits error", err)
			}
		})
	}
}

func TestAdapterConvertsMessagesToolsAndDelegates(t *testing.T) {
	fake := &fakeProvider{}
	adapter := New(fake, Config{Model: "gpt-test", MaxTokens: 321, Temperature: 0.4})

	_, err := adapter.Start(context.Background(), agentruntime.ModelRequest{
		SessionID:     "session-1",
		TurnID:        "turn-1",
		SystemPrompts: []string{"skill discovery", "project instructions"},
		Messages: []agentruntime.Message{
			{Type: agentruntime.MessageTypeSystem, Content: "system"},
			{Type: agentruntime.MessageTypeUser, Content: "user"},
			{Type: agentruntime.MessageTypeAssistant, Content: "assistant"},
			{
				Type:    agentruntime.MessageTypeToolCall,
				Content: "calling tools",
				ToolCalls: []agentruntime.ToolCall{
					{CallID: "call-1", Name: "weather", Arguments: json.RawMessage(`{"city":"Bangkok"}`)},
					{CallID: "call-2", Name: "clock", Arguments: json.RawMessage(`{"zone":"UTC"}`)},
				},
			},
			{Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{
				CallID: "call-1", Name: "weather", Status: agentruntime.ToolResultSucceeded, Output: json.RawMessage(`{"temperature":31}`),
			}},
			{Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{
				CallID: "call-2", Name: "clock", Status: agentruntime.ToolResultFailed, Error: "service unavailable",
			}},
			{Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{
				CallID: "call-3", Name: "slow", Status: agentruntime.ToolResultInterrupted, Error: "cancelled",
			}},
			{Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{
				CallID: "call-4", Name: "write", Status: agentruntime.ToolResultDenied, Error: "permission denied",
			}},
		},
		Tools: []agentruntime.ToolDefinition{{
			Name:        "weather",
			Description: "Looks up weather",
			InputSchema: agentruntime.ToolSchema{
				Type: "object",
				Properties: map[string]agentruntime.ToolSchema{
					"city": {Type: "string"},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if fake.streamCalls != 1 {
		t.Fatalf("provider Stream calls = %d, want 1", fake.streamCalls)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(fake.requests))
	}

	request := fake.requests[0]
	if request.Model != "gpt-test" || request.MaxTokens != 321 || request.Temperature != 0.4 {
		t.Fatalf("provider request options = %#v", request)
	}
	if len(request.Messages) != 10 {
		t.Fatalf("messages = %#v", request.Messages)
	}
	for index, role := range []string{"system", "system", "system", "user", "assistant", "assistant", "tool", "tool", "tool", "tool"} {
		if request.Messages[index].Role != role {
			t.Fatalf("message %d role = %q, want %q", index, request.Messages[index].Role, role)
		}
	}
	if request.Messages[0].Content != "skill discovery" || request.Messages[1].Content != "project instructions" || request.Messages[2].Content != "system" {
		t.Fatalf("system messages = %#v", request.Messages[:3])
	}
	if request.Messages[5].Content != "calling tools" || len(request.Messages[5].ToolCalls) != 2 {
		t.Fatalf("tool-call message = %#v", request.Messages[5])
	}
	if call := request.Messages[5].ToolCalls[0]; call.ID != "call-1" || call.Type != "function" || call.Name != "weather" || call.Arguments["city"] != "Bangkok" {
		t.Fatalf("first tool call = %#v", call)
	}
	if request.Messages[6].ToolCallID != "call-1" || request.Messages[6].Content != `{"temperature":31}` {
		t.Fatalf("successful tool result = %#v", request.Messages[6])
	}
	for _, index := range []int{7, 8, 9} {
		var failure struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal([]byte(request.Messages[index].Content), &failure); err != nil {
			t.Fatalf("decode failure result %d: %v", index, err)
		}
		wantStatus := []string{"failed", "interrupted", "denied"}[index-7]
		if failure.Status != wantStatus || failure.Error == "" {
			t.Fatalf("failure result %d = %#v", index, failure)
		}
	}
	if len(request.ToolSchema) != 1 || request.ToolSchema[0].Function == nil {
		t.Fatalf("tool schemas = %#v", request.ToolSchema)
	}
	function := request.ToolSchema[0].Function
	if function.Name != "weather" || function.Description != "Looks up weather" {
		t.Fatalf("function schema = %#v", function)
	}
	parameters, err := json.Marshal(function.Parameters)
	if err != nil {
		t.Fatalf("marshal function parameters: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(parameters, &schema); err != nil || schema["type"] != "object" {
		t.Fatalf("function parameters = %s, err = %v", parameters, err)
	}
}

func TestAdapterForwardsReasoningWithoutSharingConfigPointer(t *testing.T) {
	disabled := false
	fake := &fakeProvider{}
	adapter := New(fake, Config{Model: "qwen-test", Reasoning: &disabled})
	disabled = true

	if _, err := adapter.Start(context.Background(), agentruntime.ModelRequest{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := fake.requests[0].Reasoning; got == nil || *got {
		t.Fatalf("request reasoning = %#v, want false", got)
	}
}

func TestAdapterRequestMaxOutputTokensOverridesConfig(t *testing.T) {
	fake := &fakeProvider{}
	adapter := New(fake, Config{Model: "gpt-test", MaxTokens: 321})
	if _, err := adapter.Start(context.Background(), agentruntime.ModelRequest{MaxOutputTokens: 123}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := fake.requests[0].MaxTokens; got != 123 {
		t.Fatalf("request MaxTokens = %d, want request limit 123", got)
	}

	if _, err := adapter.Start(context.Background(), agentruntime.ModelRequest{}); err != nil {
		t.Fatalf("Start with no request limit: %v", err)
	}
	if got := fake.requests[1].MaxTokens; got != 321 {
		t.Fatalf("request MaxTokens = %d, want config fallback 321", got)
	}
}

func TestAdapterRejectsMalformedInputsBeforeProvider(t *testing.T) {
	tests := []struct {
		name    string
		request agentruntime.ModelRequest
	}{
		{
			name: "tool call arguments",
			request: agentruntime.ModelRequest{Messages: []agentruntime.Message{{
				Type:      agentruntime.MessageTypeToolCall,
				ToolCalls: []agentruntime.ToolCall{{CallID: "call-1", Name: "weather", Arguments: json.RawMessage("not-json")}},
			}}},
		},
		{
			name: "successful result output",
			request: agentruntime.ModelRequest{Messages: []agentruntime.Message{{
				Type:       agentruntime.MessageTypeToolResult,
				ToolResult: &agentruntime.ToolResult{CallID: "call-1", Name: "weather", Status: agentruntime.ToolResultSucceeded, Output: json.RawMessage("not-json")},
			}}},
		},
		{
			name: "invalid tool schema",
			request: agentruntime.ModelRequest{Tools: []agentruntime.ToolDefinition{{
				Name: "weather", InputSchema: agentruntime.ToolSchema{Type: "object", Types: []string{"object"}},
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeProvider{}
			adapter := New(fake, Config{Model: "gpt-test"})
			if _, err := adapter.Start(context.Background(), test.request); err == nil {
				t.Fatal("expected Start error")
			}
			if fake.streamCalls != 0 {
				t.Fatalf("provider Stream calls = %d, want 0", fake.streamCalls)
			}
		})
	}
}

func TestAdapterRequiresModelBeforeProvider(t *testing.T) {
	fake := &fakeProvider{}
	adapter := New(fake, Config{})
	if _, err := adapter.Start(context.Background(), agentruntime.ModelRequest{}); err == nil {
		t.Fatal("expected missing model error")
	}
	if fake.streamCalls != 0 {
		t.Fatalf("provider Stream calls = %d, want 0", fake.streamCalls)
	}
}

func TestTransformMessagesMapsRuntimeEventToProviderInput(t *testing.T) {
	converted, err := transformMessages([]agentruntime.Message{{
		Type: agentruntime.MessageTypeRuntimeEvent, Content: "<subagent_callback>done</subagent_callback>",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != 1 || converted[0].Role != "user" || converted[0].Content != "<subagent_callback>done</subagent_callback>" {
		t.Fatalf("converted runtime event = %#v", converted)
	}
}

func TestTransformMessagesDropsLegacyEmptyTextMessages(t *testing.T) {
	converted, err := transformMessages([]agentruntime.Message{
		{Type: agentruntime.MessageTypeSystem},
		{Type: agentruntime.MessageTypeUser, Content: " \t\n "},
		{Type: agentruntime.MessageTypeAssistant},
		{Type: agentruntime.MessageTypeRuntimeEvent},
		{Type: agentruntime.MessageTypeUser, Content: "hello"},
		{
			Type: agentruntime.MessageTypeToolCall,
			ToolCalls: []agentruntime.ToolCall{{
				CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{}`),
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != 2 {
		t.Fatalf("converted messages = %#v, want user and tool-call messages", converted)
	}
	if converted[0].Role != "user" || converted[0].Content != "hello" {
		t.Fatalf("converted user message = %#v", converted[0])
	}
	if converted[1].Role != "assistant" || len(converted[1].ToolCalls) != 1 || converted[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("converted tool-call message = %#v", converted[1])
	}
}

func TestAdapterPlacesContextRemindersWithoutMutatingTranscript(t *testing.T) {
	t.Run("tail reminder preserves tool adjacency", func(t *testing.T) {
		fake := &fakeProvider{}
		adapter := New(fake, Config{Model: "gpt-test"})
		messages := []agentruntime.Message{
			{Type: agentruntime.MessageTypeUser, Content: "first user"},
			{Type: agentruntime.MessageTypeAssistant, Content: "ack"},
			{Type: agentruntime.MessageTypeUser, Content: "latest user"},
			{Type: agentruntime.MessageTypeToolCall, ToolCalls: []agentruntime.ToolCall{{CallID: "call-1", Name: "weather", Arguments: json.RawMessage(`{}`)}}},
			{Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{CallID: "call-1", Name: "weather", Status: agentruntime.ToolResultSucceeded, Output: json.RawMessage(`null`)}},
		}
		reminders := []agentruntime.ContextReminder{{Content: "<active_subagents>one</active_subagents>"}, {Content: "second"}}
		if _, err := adapter.Start(context.Background(), agentruntime.ModelRequest{Messages: messages, ContextReminders: reminders}); err != nil {
			t.Fatal(err)
		}
		if got := messages[2].Content; got != "latest user" {
			t.Fatalf("input latest user content = %q, want unchanged", got)
		}
		if len(fake.requests) != 1 {
			t.Fatalf("provider requests = %d, want 1", len(fake.requests))
		}
		got := fake.requests[0].Messages
		if len(got) != 6 || got[2].Role != "user" || got[2].Content != "latest user" {
			t.Fatalf("provider messages = %#v", got)
		}
		wantContent := "<system-reminder>\n<active_subagents>one</active_subagents>\n</system-reminder>\n\n<system-reminder>\nsecond\n</system-reminder>"
		if got[5].Role != "user" || got[5].Content != wantContent {
			t.Fatalf("tail reminder = %#v, want content %q", got[5], wantContent)
		}
		if got[3].Role != "assistant" || len(got[3].ToolCalls) != 1 || got[3].ToolCalls[0].ID != "call-1" || got[4].Role != "tool" || got[4].ToolCallID != "call-1" {
			t.Fatalf("tool call/result adjacency changed: %#v", got[3:])
		}
	})

	t.Run("repair reminder separates trailing assistant messages", func(t *testing.T) {
		fake := &fakeProvider{}
		adapter := New(fake, Config{Model: "qwen-test"})
		messages := []agentruntime.Message{
			{Type: agentruntime.MessageTypeUser, Content: "hello"},
			{Type: agentruntime.MessageTypeAssistant, Content: "first answer"},
			{Type: agentruntime.MessageTypeAssistant, Content: "repair answer"},
		}
		if _, err := adapter.Start(context.Background(), agentruntime.ModelRequest{
			Messages:         messages,
			ContextReminders: []agentruntime.ContextReminder{{Content: "call report_discord"}},
		}); err != nil {
			t.Fatal(err)
		}
		got := fake.requests[0].Messages
		if len(got) != 4 {
			t.Fatalf("provider messages = %#v", got)
		}
		for index, role := range []string{"user", "assistant", "assistant", "user"} {
			if got[index].Role != role {
				t.Fatalf("message %d role = %q, want %q: %#v", index, got[index].Role, role, got)
			}
		}
		if got[3].Content != "<system-reminder>\ncall report_discord\n</system-reminder>" {
			t.Fatalf("repair reminder = %#v", got[3])
		}
	})

	t.Run("trailing user receives reminder in place", func(t *testing.T) {
		fake := &fakeProvider{}
		adapter := New(fake, Config{Model: "gpt-test"})
		if _, err := adapter.Start(context.Background(), agentruntime.ModelRequest{
			Messages:         []agentruntime.Message{{Type: agentruntime.MessageTypeUser, Content: "hello"}},
			ContextReminders: []agentruntime.ContextReminder{{Content: "trusted"}},
		}); err != nil {
			t.Fatal(err)
		}
		got := fake.requests[0].Messages
		want := "hello\n\n<system-reminder>\ntrusted\n</system-reminder>"
		if len(got) != 1 || got[0].Role != "user" || got[0].Content != want {
			t.Fatalf("provider messages = %#v, want trailing user reminder", got)
		}
	})

	t.Run("no user uses system fallback", func(t *testing.T) {
		fake := &fakeProvider{}
		adapter := New(fake, Config{Model: "gpt-test"})
		if _, err := adapter.Start(context.Background(), agentruntime.ModelRequest{
			Messages:         []agentruntime.Message{{Type: agentruntime.MessageTypeAssistant, Content: "prior"}},
			ContextReminders: []agentruntime.ContextReminder{{Content: "trusted"}},
		}); err != nil {
			t.Fatal(err)
		}
		got := fake.requests[0].Messages
		if len(got) != 2 || got[0].Role != "system" || got[0].Content != "<system-reminder>\ntrusted\n</system-reminder>" || got[1].Role != "assistant" || got[1].Content != "prior" {
			t.Fatalf("fallback messages = %#v", got)
		}
	})
}

type fakeProvider struct {
	requests    []provideropenai.Request
	streamCalls int
}

func (p *fakeProvider) Stream(_ context.Context, request provideropenai.Request) (provider.ChunkStream[sdkopenai.ChatCompletionStreamResponse], error) {
	p.streamCalls++
	p.requests = append(p.requests, request)
	return eofChunkStream{}, nil
}

func (*fakeProvider) Parse(sdkopenai.ChatCompletionStreamResponse) ([]provider.StreamEvent, error) {
	return nil, nil
}

type eofChunkStream struct{}

func (eofChunkStream) Recv() (sdkopenai.ChatCompletionStreamResponse, error) {
	return sdkopenai.ChatCompletionStreamResponse{}, io.EOF
}

func (eofChunkStream) Close() error { return nil }
