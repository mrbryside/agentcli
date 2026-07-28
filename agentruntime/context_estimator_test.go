package agentruntime

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGenericContextEstimatorCountsEveryRequestSurface(t *testing.T) {
	estimator := GenericContextEstimator{}
	base, err := estimator.Estimate(ModelRequest{})
	if err != nil {
		t.Fatalf("estimate empty request: %v", err)
	}
	request := ModelRequest{
		SystemPrompts:    []string{"system instructions"},
		ContextReminders: []ContextReminder{{Content: "transient reminder"}},
		Messages: []Message{{
			Type:      MessageTypeToolCall,
			Content:   "calling tool",
			Reasoning: "reasoning",
			ToolCalls: []ToolCall{{CallID: "call-1", Name: "lookup", Arguments: []byte(`{"query":"example"}`)}},
		}, {
			Type: MessageTypeToolResult,
			ToolResult: &ToolResult{
				CallID: "call-1", Name: "lookup", Status: ToolResultSucceeded, Output: []byte(`{"result":"found"}`),
			},
		}},
		Tools: []ToolDefinition{{
			Name: "lookup", Description: "searches records", InputSchema: ToolSchema{Type: "object", Properties: map[string]ToolSchema{"query": {Type: "string"}}},
		}},
	}
	estimate, err := estimator.Estimate(request)
	if err != nil {
		t.Fatalf("estimate request: %v", err)
	}
	if estimate.Tokens <= base.Tokens {
		t.Fatalf("estimate = %d, empty = %d; expected all request surfaces to add tokens", estimate.Tokens, base.Tokens)
	}
	if err := estimate.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	withoutPrompts := request.Clone()
	withoutPrompts.SystemPrompts = nil
	if reduced, err := estimator.Estimate(withoutPrompts); err != nil || reduced.Tokens >= estimate.Tokens {
		t.Fatalf("removing prompts estimate = %#v, %v; want fewer than %d", reduced, err, estimate.Tokens)
	}
	withoutReminders := request.Clone()
	withoutReminders.ContextReminders = nil
	if reduced, err := estimator.Estimate(withoutReminders); err != nil || reduced.Tokens >= estimate.Tokens {
		t.Fatalf("removing reminders estimate = %#v, %v; want fewer than %d", reduced, err, estimate.Tokens)
	}
	withoutMessages := request.Clone()
	withoutMessages.Messages = nil
	if reduced, err := estimator.Estimate(withoutMessages); err != nil || reduced.Tokens >= estimate.Tokens {
		t.Fatalf("removing messages estimate = %#v, %v; want fewer than %d", reduced, err, estimate.Tokens)
	}
	withoutTools := request.Clone()
	withoutTools.Tools = nil
	if reduced, err := estimator.Estimate(withoutTools); err != nil || reduced.Tokens >= estimate.Tokens {
		t.Fatalf("removing tools estimate = %#v, %v; want fewer than %d", reduced, err, estimate.Tokens)
	}
}

func TestGenericContextEstimatorRejectsInvalidToolSchema(t *testing.T) {
	_, err := (GenericContextEstimator{}).Estimate(ModelRequest{Tools: []ToolDefinition{{
		Name:        "bad",
		InputSchema: ToolSchema{Type: "object", TypeUnion: []string{"object"}},
	}}})
	if err == nil {
		t.Fatal("Estimate() succeeded with invalid tool schema")
	}
}

func TestGenericContextEstimatorConservativelyCountsMultilingualText(t *testing.T) {
	estimator := GenericContextEstimator{}
	for _, text := range []string{
		"ภาษาไทย",
		"中文測試",
		"😀🎉🚀",
	} {
		t.Run(text, func(t *testing.T) {
			base, err := estimator.Estimate(ModelRequest{})
			if err != nil {
				t.Fatalf("estimate empty request: %v", err)
			}
			estimate, err := estimator.Estimate(ModelRequest{SystemPrompts: []string{text}})
			if err != nil {
				t.Fatalf("estimate multilingual prompt: %v", err)
			}
			contentTokens := estimate.Tokens - base.Tokens - genericPromptOverhead
			if floor := utf8.RuneCountInString(text); contentTokens < floor {
				t.Fatalf("content estimate = %d tokens, want at least %d per-rune floor for %q", contentTokens, floor, text)
			}
		})
	}
}

func TestGenericContextEstimatorConservativelyCountsStructuredASCII(t *testing.T) {
	text := strings.Repeat(`{"tool_result":"abcdefghijklmnopqrstuvwxyz"}`, 1_000)
	estimate, err := (GenericContextEstimator{}).Estimate(ModelRequest{
		Messages: []Message{{Type: MessageTypeToolResult, ToolResult: &ToolResult{
			CallID: "call", Name: "tool", Status: ToolResultSucceeded, Output: []byte(text),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	minimum := (len(text) + genericCharactersPerToken - 1) / genericCharactersPerToken
	if estimate.Tokens < minimum {
		t.Fatalf("estimate = %d; want at least %d for %d structured ASCII bytes", estimate.Tokens, minimum, len(text))
	}
	if genericCharactersPerToken != 3 {
		t.Fatalf("generic ASCII divisor = %d; want conservative provider-neutral divisor 3", genericCharactersPerToken)
	}
}

func TestContextEstimatorFuncClonesAndValidates(t *testing.T) {
	request := ModelRequest{
		SystemPrompts: []string{"original"},
		Messages:      []Message{{Content: "original"}},
		Tools:         []ToolDefinition{{Name: "tool", InputSchema: ToolSchema{Type: "object"}}},
	}
	estimator := ContextEstimatorFunc(func(got ModelRequest) (ContextEstimate, error) {
		got.SystemPrompts[0] = "changed"
		got.Messages[0].Content = "changed"
		got.Tools[0].InputSchema.Type = "string"
		return ContextEstimate{Tokens: 12}, nil
	})
	estimate, err := estimator.Estimate(request)
	if err != nil {
		t.Fatalf("Estimate() error = %v", err)
	}
	if estimate.Tokens != 12 {
		t.Fatalf("Estimate() = %#v", estimate)
	}
	if request.SystemPrompts[0] != "original" || request.Messages[0].Content != "original" || request.Tools[0].InputSchema.Type != "object" {
		t.Fatalf("estimator mutated original request: %#v", request)
	}

	_, err = ContextEstimatorFunc(func(ModelRequest) (ContextEstimate, error) {
		return ContextEstimate{Tokens: -1}, nil
	}).Estimate(request)
	if !errors.Is(err, ErrInvalidContextEstimate) {
		t.Fatalf("negative estimate error = %v, want ErrInvalidContextEstimate", err)
	}
}

func TestModelRequestCloneIsDeep(t *testing.T) {
	request := ModelRequest{
		SystemPrompts:    []string{"prompt"},
		ContextReminders: []ContextReminder{{Content: "reminder"}},
		Messages: []Message{{
			Type:       MessageTypeToolResult,
			ToolResult: &ToolResult{CallID: "call", Name: "tool", Status: ToolResultSucceeded, Output: []byte(`{"ok":true}`)},
		}},
		Tools: []ToolDefinition{{Name: "tool", InputSchema: ToolSchema{Type: "object", Properties: map[string]ToolSchema{"value": {Type: "string"}}}}},
	}
	clone := request.Clone()
	clone.SystemPrompts[0] = "changed"
	clone.ContextReminders[0].Content = "changed"
	clone.Messages[0].ToolResult.Output[0] = '['
	clone.Tools[0].InputSchema.Properties["value"] = ToolSchema{Type: "number"}
	if request.SystemPrompts[0] != "prompt" || request.ContextReminders[0].Content != "reminder" {
		t.Fatal("clone changed request slices")
	}
	if string(request.Messages[0].ToolResult.Output) != `{"ok":true}` {
		t.Fatal("clone changed request message JSON")
	}
	if request.Tools[0].InputSchema.Properties["value"].Type != "string" {
		t.Fatal("clone changed request tool schema")
	}
}

var _ ContextEstimator = ContextEstimatorFunc(nil)
var _ ContextEstimator = GenericContextEstimator{}
