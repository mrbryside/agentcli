package toolexecution

import (
	"context"
	"encoding/json"
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
		{name: "unsupported turn behavior", tool: Tool{Definition: validDefinition, Handler: testHandler, TurnBehavior: "later"}},
		{name: "required finalizer must end turn", tool: Tool{Definition: validDefinition, Handler: testHandler, RequiredAtTurnEnd: true}},
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
