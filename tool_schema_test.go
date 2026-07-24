package agentcli

import (
	"encoding/json"
	"testing"
)

func TestObjectSchemaBuildsDescribedParameters(t *testing.T) {
	type readParameters struct {
		Path   ToolParameter
		Offset ToolParameter `json:"offset"`
		Limit  ToolParameter
	}
	schema := ObjectSchema(readParameters{
		Path:   StringParameter("Project-relative file path").Required().MinLength(1),
		Offset: IntegerParameter("First 1-based line").Minimum(1),
		Limit:  IntegerParameter("Maximum lines").Minimum(1).Maximum(2000),
	})
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	if actual["type"] != "object" || actual["additionalProperties"] != false {
		t.Fatalf("unexpected object schema: %s", encoded)
	}
	properties := actual["properties"].(map[string]any)
	if properties["path"].(map[string]any)["description"] != "Project-relative file path" {
		t.Fatalf("missing parameter description: %s", encoded)
	}
	required := actual["required"].([]any)
	if len(required) != 1 || required[0] != "path" {
		t.Fatalf("unexpected required fields: %s", encoded)
	}
}

func TestObjectSchemaSupportsEveryTypedSchemaFeature(t *testing.T) {
	schema := ObjectSchema(struct {
		Payload ToolParameter
	}{Payload: Parameter(InputSchema{
		TypeUnion: []string{"string", "null"}, Format: "email", ContentEncoding: "base64",
		Enum:  []json.RawMessage{json.RawMessage(`"a"`), json.RawMessage(`null`)},
		AllOf: []InputSchema{{Pattern: "^[a-z]+$"}},
	}, "An optional payload")})
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("invalid schema: %s", encoded)
	}
}

func TestTryObjectSchemaRejectsInvalidFields(t *testing.T) {
	_, err := TryObjectSchema(struct{ Path string }{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRawInputSchema(t *testing.T) {
	schema, err := RawInputSchema(json.RawMessage(`{"type":"object","x-vendor":true}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"type":"object","x-vendor":true}` {
		t.Fatalf("raw schema changed: %s", encoded)
	}
}
