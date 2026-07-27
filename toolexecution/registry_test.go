package toolexecution

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
)

func TestRegistryRegisterDefinitionsAndLookup(t *testing.T) {
	registry := NewRegistry()
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}

	first := Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        "weather",
			Description: "Get the weather",
			InputSchema: mustRawToolSchema(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		},
		Handler: handler,
	}
	second := Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        "time",
			Description: "Get the time",
			InputSchema: mustRawToolSchema(`{"type":"object"}`),
		},
		Handler: handler,
	}
	if err := registry.Register(first); err != nil {
		t.Fatalf("Register(first) error = %v", err)
	}
	if err := registry.Register(second); err != nil {
		t.Fatalf("Register(second) error = %v", err)
	}

	definitions := registry.Definitions()
	if len(definitions) != 2 {
		t.Fatalf("Definitions() length = %d, want 2", len(definitions))
	}
	if definitions[0].Name != "weather" || definitions[1].Name != "time" {
		t.Fatalf("Definitions() order = %v, want weather then time", []string{definitions[0].Name, definitions[1].Name})
	}

	got, ok := registry.lookup("weather")
	if !ok || got == nil {
		t.Fatalf("lookup(weather) = (%v, %v), want registered handler", got, ok)
	}
	if _, ok := registry.lookup("missing"); ok {
		t.Fatal("lookup(missing) found a handler")
	}
}

func TestRegistryDefinitionsDoNotShareSchemas(t *testing.T) {
	registry := NewRegistry()
	definition := agentruntime.ToolDefinition{
		Name:        "weather",
		Description: "Get the weather",
		InputSchema: agentruntime.ToolSchema{Type: "object", Properties: map[string]agentruntime.ToolSchema{"city": {Type: "string"}}},
	}
	if err := registry.Register(Tool{Definition: definition, Handler: testHandler}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	definition.InputSchema.Properties["city"] = agentruntime.ToolSchema{Type: "number"}
	definitions := registry.Definitions()
	if definitions[0].InputSchema.Properties["city"].Type != "string" {
		t.Fatalf("registered schema = %#v, want independent copy", definitions[0].InputSchema)
	}

	definitions[0].InputSchema.Properties["city"] = agentruntime.ToolSchema{Type: "boolean"}
	fresh := registry.Definitions()
	if fresh[0].InputSchema.Properties["city"].Type != "string" {
		t.Fatalf("Definitions() schema = %#v, want independent copy", fresh[0].InputSchema)
	}
}

func TestRegistryInjectsTriggerGuidanceIntoToolDescriptions(t *testing.T) {
	tests := []struct {
		name             string
		trigger          ToolTrigger
		endTurnOnSuccess bool
		contains         []string
		excludes         []string
	}{
		{
			name:     "immediate default",
			contains: []string{"Application description."},
			excludes: []string{"Runtime trigger", "Runtime turn behavior"},
		},
		{
			name:    "end turn",
			trigger: EndTurn,
			contains: []string{
				"Application description.",
				"Runtime trigger (end_turn)",
				"when the current turn is ready to finish",
				"handler runs immediately",
				"does not end the current turn automatically",
			},
		},
		{
			name:    "end response scope",
			trigger: EndResponseScope,
			contains: []string{
				"Application description.",
				"Runtime behavior (end_response_scope): This is a final-response tool. Call it only after all other work is complete and you are ready to finish the response. If called earlier, the tool action is skipped and the result is still treated as success. Do not retry it yourself; continue the remaining work until the runtime requests the final call.",
				"This is a final-response tool",
				"Call it only after all other work is complete",
				"the tool action is skipped",
				"the result is still treated as success",
				"Do not retry it yourself",
				"runtime requests the final call",
				"does not end the current turn automatically",
			},
		},
		{
			name:             "end on success",
			endTurnOnSuccess: true,
			contains: []string{
				"Application description.",
				"Runtime turn behavior (end_on_success)",
				"every result in the same tool batch succeeds",
				"current turn ends",
			},
			excludes: []string{"Runtime trigger"},
		},
		{
			name:             "end response scope and end on success",
			trigger:          EndResponseScope,
			endTurnOnSuccess: true,
			contains: []string{
				"Runtime behavior (end_response_scope): This is a final-response tool. Call it only after all other work is complete and you are ready to finish the response. If called earlier, the tool action is skipped and the result is still treated as success. Do not retry it yourself; continue the remaining work until the runtime requests the final call.",
				"Runtime turn behavior (end_on_success)",
				"successful final-boundary execution ends the current turn",
				"waiting for callbacks or other active turns",
				"scope is otherwise quiescent",
				"continues the current turn so remaining work can finish",
			},
			excludes: []string{"does not end the current turn automatically"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			tool := Tool{
				Definition: agentruntime.ToolDefinition{
					Name:        "report",
					Description: "Application description.",
					InputSchema: agentruntime.ToolSchema{Type: "object"},
				},
				Handler:          testHandler,
				Trigger:          test.trigger,
				EndTurnOnSuccess: test.endTurnOnSuccess,
			}
			if err := registry.Register(tool); err != nil {
				t.Fatal(err)
			}
			if tool.Definition.Description != "Application description." {
				t.Fatalf("Register mutated caller definition: %q", tool.Definition.Description)
			}
			description := registry.Definitions()[0].Description
			for _, expected := range test.contains {
				if !strings.Contains(description, expected) {
					t.Errorf("description %q does not contain %q", description, expected)
				}
			}
			for _, forbidden := range test.excludes {
				if strings.Contains(description, forbidden) {
					t.Errorf("description %q contains %q", description, forbidden)
				}
			}
		})
	}
}

func TestRegistryRegisterRejectsInvalidTools(t *testing.T) {
	validDefinition := agentruntime.ToolDefinition{
		Name:        "weather",
		InputSchema: agentruntime.ToolSchema{Type: "object"},
	}
	tests := []struct {
		name string
		tool Tool
	}{
		{name: "empty name", tool: Tool{Definition: agentruntime.ToolDefinition{InputSchema: validDefinition.InputSchema}, Handler: testHandler}},
		{name: "nil handler", tool: Tool{Definition: validDefinition}},
		{name: "unsupported trigger", tool: Tool{Definition: validDefinition, Handler: testHandler, Trigger: "later"}},
		{name: "function and prompt call guards", tool: Tool{Definition: validDefinition, Handler: testHandler, ToolCallGuard: func(context.Context, agentruntime.ToolCallGuardAttempt) (agentruntime.ToolCallGuardDecision, error) {
			return agentruntime.ToolCallGuardDecision{Action: agentruntime.ToolCallAllow}, nil
		}, ToolCallGuardPrompt: "check call"}},
		{name: "whitespace call guard prompt", tool: Tool{Definition: validDefinition, Handler: testHandler, ToolCallGuardPrompt: " \n\t "}},
		{name: "call guard model without provider", tool: Tool{Definition: validDefinition, Handler: testHandler, ToolCallGuardPrompt: "check call", ToolCallGuardModel: &GuardModelConfig{Model: "guard-model"}}},
		{name: "call guard model without name", tool: Tool{Definition: validDefinition, Handler: testHandler, ToolCallGuardPrompt: "check call", ToolCallGuardModel: &GuardModelConfig{Provider: "policy"}}},
		{name: "call guard model without prompt", tool: Tool{Definition: validDefinition, Handler: testHandler, ToolCallGuardModel: &GuardModelConfig{Provider: "policy", Model: "guard-model"}}},
		{name: "whitespace call guard provider", tool: Tool{Definition: validDefinition, Handler: testHandler, ToolCallGuardPrompt: "check call", ToolCallGuardModel: &GuardModelConfig{Provider: " \n", Model: "guard-model"}}},
		{name: "whitespace call guard model", tool: Tool{Definition: validDefinition, Handler: testHandler, ToolCallGuardPrompt: "check call", ToolCallGuardModel: &GuardModelConfig{Provider: "policy", Model: " \n"}}},
		{name: "array schema", tool: Tool{Definition: agentruntime.ToolDefinition{Name: "array", InputSchema: agentruntime.ToolSchema{Type: "array"}}, Handler: testHandler}},
		{name: "non-object type", tool: Tool{Definition: agentruntime.ToolDefinition{Name: "string", InputSchema: agentruntime.ToolSchema{Type: "string"}}, Handler: testHandler}},
		{name: "missing type", tool: Tool{Definition: agentruntime.ToolDefinition{Name: "type", InputSchema: agentruntime.ToolSchema{Properties: map[string]agentruntime.ToolSchema{}}}, Handler: testHandler}},
		{name: "invalid schema", tool: Tool{Definition: agentruntime.ToolDefinition{Name: "invalid", InputSchema: agentruntime.ToolSchema{Type: "object", Types: []string{"object"}}}, Handler: testHandler}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := NewRegistry().Register(test.tool); err == nil {
				t.Fatal("Register() error = nil, want rejection")
			}
		})
	}

	registry := NewRegistry()
	if err := registry.Register(Tool{Definition: validDefinition, Handler: testHandler}); err != nil {
		t.Fatalf("Register(valid) error = %v", err)
	}
	if err := registry.Register(Tool{Definition: validDefinition, Handler: testHandler}); err == nil {
		t.Fatal("Register(duplicate) error = nil, want rejection")
	}
}

func TestRegistryTriggerModesApplyCompletionPolicy(t *testing.T) {
	for _, test := range []struct {
		name     string
		trigger  ToolTrigger
		behavior agentruntime.ToolTurnBehavior
	}{
		{name: "default continues", behavior: agentruntime.ToolTurnContinue},
		{name: "end turn trigger continues", trigger: EndTurn, behavior: agentruntime.ToolTurnContinue},
		{name: "end response scope trigger continues", trigger: EndResponseScope, behavior: agentruntime.ToolTurnContinue},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			if err := registry.Register(Tool{
				Definition: agentruntime.ToolDefinition{Name: "report", InputSchema: agentruntime.ToolSchema{Type: "object"}},
				Handler:    testHandler,
				Trigger:    test.trigger,
			}); err != nil {
				t.Fatal(err)
			}
			if trigger, ok := registry.triggerFor("report"); !ok || trigger != test.trigger {
				t.Fatalf("trigger = (%q, %t)", trigger, ok)
			}
			if behavior, ok := registry.turnBehaviorFor("report", nil, nil); !ok || behavior != test.behavior {
				t.Fatalf("turn behavior = (%q, %t), want %q", behavior, ok, test.behavior)
			}
		})
	}
}

func TestRegistryEndTurnOnSuccessAppliesIndependentlyFromTrigger(t *testing.T) {
	for _, trigger := range []ToolTrigger{"", EndTurn, EndResponseScope} {
		t.Run(string(trigger), func(t *testing.T) {
			registry := NewRegistry()
			if err := registry.Register(Tool{
				Definition:       agentruntime.ToolDefinition{Name: "handoff", InputSchema: agentruntime.ToolSchema{Type: "object"}},
				Handler:          testHandler,
				Trigger:          trigger,
				EndTurnOnSuccess: true,
			}); err != nil {
				t.Fatal(err)
			}
			if got, ok := registry.triggerFor("handoff"); !ok || got != trigger {
				t.Fatalf("trigger = (%q, %t), want %q", got, ok, trigger)
			}
			if behavior, ok := registry.turnBehaviorFor("handoff", nil, nil); !ok || behavior != agentruntime.ToolTurnEndOnSuccess {
				t.Fatalf("turn behavior = (%q, %t), want %q", behavior, ok, agentruntime.ToolTurnEndOnSuccess)
			}
		})
	}
}

func TestToolExposesTriggerAndEndTurnOnSuccessOnly(t *testing.T) {
	toolType := reflect.TypeOf(Tool{})
	if _, found := toolType.FieldByName("Trigger"); !found {
		t.Fatal("Tool has no Trigger field")
	}
	if field, found := toolType.FieldByName("EndTurnOnSuccess"); !found || field.Type.Kind() != reflect.Bool {
		t.Fatal("Tool has no boolean EndTurnOnSuccess field")
	}
	for _, removed := range []string{"Lifecycle", "TurnBehavior", "RequiredAtTurnEnd"} {
		if _, found := toolType.FieldByName(removed); found {
			t.Fatalf("Tool still exposes legacy completion field %q", removed)
		}
	}
}

func testHandler(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func marshaledToolSchema(t *testing.T, schema agentruntime.ToolSchema) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal tool schema: %v", err)
	}
	return encoded
}
