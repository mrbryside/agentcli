package agentruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ToolSchema is the provider-neutral JSON Schema vocabulary used to describe
// a tool's parameters. A schema may be composed recursively. The zero value is
// an empty schema (which is valid JSON Schema).
//
// Type is the single-type form. TypeUnion (also available as Types) is the
// JSON Schema array form (for example []string{"string", "null"}); setting a
// single and union form together is invalid.
type ToolSchema struct {
	Type             string
	TypeUnion        []string
	Types            []string
	Title            string
	Description      string
	Format           string
	Pattern          string
	ContentEncoding  string
	ContentMediaType string
	ContentSchema    *ToolSchema

	Enum     []json.RawMessage
	Const    json.RawMessage
	Default  json.RawMessage
	Examples []json.RawMessage

	MinLength        json.Number
	MaxLength        json.Number
	Minimum          json.Number
	Maximum          json.Number
	ExclusiveMinimum json.Number
	ExclusiveMaximum json.Number
	MultipleOf       json.Number

	Properties            map[string]ToolSchema
	Required              []string
	AdditionalProperties  *ToolSchemaAdditionalProperties
	PatternProperties     map[string]ToolSchema
	PropertyNames         *ToolSchema
	MinProperties         json.Number
	MaxProperties         json.Number
	UnevaluatedProperties *ToolSchemaAdditionalProperties

	Items            *ToolSchema
	PrefixItems      []ToolSchema
	Contains         *ToolSchema
	MinContains      json.Number
	MaxContains      json.Number
	MinItems         json.Number
	MaxItems         json.Number
	UniqueItems      *bool
	UnevaluatedItems *ToolSchemaAdditionalProperties

	AllOf []ToolSchema
	AnyOf []ToolSchema
	OneOf []ToolSchema
	Not   *ToolSchema

	Schema string
	ID     string
	Anchor string
	Ref    string
	Defs   map[string]ToolSchema

	ReadOnly   *bool
	WriteOnly  *bool
	Deprecated *bool

	raw json.RawMessage
}

// ToolSchemaAdditionalProperties represents JSON Schema's boolean-or-schema
// additionalProperties and unevaluatedProperties keywords. Exactly one member
// must be set.
type ToolSchemaAdditionalProperties struct {
	Allowed *bool
	Schema  *ToolSchema
}

// AdditionalPropertiesBool creates a boolean additionalProperties value.
func AdditionalPropertiesBool(allowed bool) *ToolSchemaAdditionalProperties {
	return &ToolSchemaAdditionalProperties{Allowed: &allowed}
}

// AdditionalPropertiesSchema creates a schema-valued additionalProperties value.
func AdditionalPropertiesSchema(schema ToolSchema) *ToolSchemaAdditionalProperties {
	return &ToolSchemaAdditionalProperties{Schema: &schema}
}

// RawToolSchema preserves an advanced schema that is outside the typed
// vocabulary. It must be a JSON object whose top-level type is "object", as
// required for tool parameters.
func RawToolSchema(raw json.RawMessage) (ToolSchema, error) {
	clone := append(json.RawMessage(nil), raw...)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(clone, &object); err != nil || object == nil {
		if err == nil {
			err = errors.New("schema must be a JSON object")
		}
		return ToolSchema{}, fmt.Errorf("schema must be valid JSON object: %w", err)
	}
	var schemaType string
	if err := json.Unmarshal(object["type"], &schemaType); err != nil || schemaType != "object" {
		return ToolSchema{}, errors.New("schema type must be object")
	}
	return ToolSchema{raw: clone}, nil
}

// Clone returns a deep, independent copy of the schema.
func (schema ToolSchema) Clone() ToolSchema {
	encoded, err := schema.MarshalJSON()
	if err != nil {
		return ToolSchema{}
	}
	if schema.raw != nil {
		clone, err := RawToolSchema(encoded)
		if err == nil {
			return clone
		}
	}
	var clone ToolSchema
	// Typed fields are copied through a JSON-compatible representation below;
	// direct recursive copy keeps raw values and json.Number exact.
	clone = schema
	clone.raw = nil
	clone.TypeUnion = append([]string(nil), schema.TypeUnion...)
	clone.Types = append([]string(nil), schema.Types...)
	clone.Enum = cloneRawValues(schema.Enum)
	clone.Const = cloneRawJSON(schema.Const)
	clone.Default = cloneRawJSON(schema.Default)
	clone.Examples = cloneRawValues(schema.Examples)
	clone.Required = append([]string(nil), schema.Required...)
	clone.Properties = cloneSchemaMap(schema.Properties)
	clone.PatternProperties = cloneSchemaMap(schema.PatternProperties)
	clone.Defs = cloneSchemaMap(schema.Defs)
	clone.PrefixItems = cloneSchemas(schema.PrefixItems)
	clone.AllOf, clone.AnyOf, clone.OneOf = cloneSchemas(schema.AllOf), cloneSchemas(schema.AnyOf), cloneSchemas(schema.OneOf)
	clone.ContentSchema, clone.PropertyNames, clone.Items, clone.Contains, clone.Not = cloneSchemaPtr(schema.ContentSchema), cloneSchemaPtr(schema.PropertyNames), cloneSchemaPtr(schema.Items), cloneSchemaPtr(schema.Contains), cloneSchemaPtr(schema.Not)
	clone.UnevaluatedItems = cloneAdditionalProperties(schema.UnevaluatedItems)
	clone.AdditionalProperties = cloneAdditionalProperties(schema.AdditionalProperties)
	clone.UnevaluatedProperties = cloneAdditionalProperties(schema.UnevaluatedProperties)
	clone.UniqueItems, clone.ReadOnly, clone.WriteOnly, clone.Deprecated = cloneBool(schema.UniqueItems), cloneBool(schema.ReadOnly), cloneBool(schema.WriteOnly), cloneBool(schema.Deprecated)
	return clone
}

func (schema ToolSchema) MarshalJSON() ([]byte, error) {
	if schema.raw != nil {
		if err := schema.rejectMixedRaw(); err != nil {
			return nil, err
		}
		if !isJSONObject(schema.raw) {
			return nil, errors.New("raw schema must be a JSON object")
		}
		return cloneRawJSON(schema.raw), nil
	}
	if (schema.Type != "" && (len(schema.TypeUnion) != 0 || len(schema.Types) != 0)) || (len(schema.TypeUnion) != 0 && len(schema.Types) != 0) {
		return nil, errors.New("schema cannot set more than one of Type, TypeUnion, and Types")
	}
	if err := validateAdditionalProperties(schema.AdditionalProperties); err != nil {
		return nil, err
	}
	if err := validateAdditionalProperties(schema.UnevaluatedProperties); err != nil {
		return nil, err
	}
	if err := validateAdditionalProperties(schema.UnevaluatedItems); err != nil {
		return nil, err
	}
	m := map[string]any{}
	put := func(k string, v any, ok bool) {
		if ok {
			m[k] = v
		}
	}
	put("type", schema.Type, schema.Type != "")
	put("type", schema.TypeUnion, len(schema.TypeUnion) != 0)
	put("type", schema.Types, len(schema.Types) != 0)
	put("title", schema.Title, schema.Title != "")
	put("description", schema.Description, schema.Description != "")
	put("format", schema.Format, schema.Format != "")
	put("pattern", schema.Pattern, schema.Pattern != "")
	put("contentEncoding", schema.ContentEncoding, schema.ContentEncoding != "")
	put("contentMediaType", schema.ContentMediaType, schema.ContentMediaType != "")
	put("contentSchema", schema.ContentSchema, schema.ContentSchema != nil)
	put("enum", schema.Enum, len(schema.Enum) != 0)
	put("const", schema.Const, schema.Const != nil)
	put("default", schema.Default, schema.Default != nil)
	put("examples", schema.Examples, len(schema.Examples) != 0)
	put("minLength", schema.MinLength, schema.MinLength != "")
	put("maxLength", schema.MaxLength, schema.MaxLength != "")
	put("minimum", schema.Minimum, schema.Minimum != "")
	put("maximum", schema.Maximum, schema.Maximum != "")
	put("exclusiveMinimum", schema.ExclusiveMinimum, schema.ExclusiveMinimum != "")
	put("exclusiveMaximum", schema.ExclusiveMaximum, schema.ExclusiveMaximum != "")
	put("multipleOf", schema.MultipleOf, schema.MultipleOf != "")
	put("properties", schema.Properties, len(schema.Properties) != 0)
	put("required", schema.Required, len(schema.Required) != 0)
	put("additionalProperties", schema.AdditionalProperties, schema.AdditionalProperties != nil)
	put("patternProperties", schema.PatternProperties, len(schema.PatternProperties) != 0)
	put("propertyNames", schema.PropertyNames, schema.PropertyNames != nil)
	put("minProperties", schema.MinProperties, schema.MinProperties != "")
	put("maxProperties", schema.MaxProperties, schema.MaxProperties != "")
	put("unevaluatedProperties", schema.UnevaluatedProperties, schema.UnevaluatedProperties != nil)
	put("items", schema.Items, schema.Items != nil)
	put("prefixItems", schema.PrefixItems, len(schema.PrefixItems) != 0)
	put("contains", schema.Contains, schema.Contains != nil)
	put("minContains", schema.MinContains, schema.MinContains != "")
	put("maxContains", schema.MaxContains, schema.MaxContains != "")
	put("minItems", schema.MinItems, schema.MinItems != "")
	put("maxItems", schema.MaxItems, schema.MaxItems != "")
	put("uniqueItems", schema.UniqueItems, schema.UniqueItems != nil)
	put("unevaluatedItems", schema.UnevaluatedItems, schema.UnevaluatedItems != nil)
	put("allOf", schema.AllOf, len(schema.AllOf) != 0)
	put("anyOf", schema.AnyOf, len(schema.AnyOf) != 0)
	put("oneOf", schema.OneOf, len(schema.OneOf) != 0)
	put("not", schema.Not, schema.Not != nil)
	put("$schema", schema.Schema, schema.Schema != "")
	put("$id", schema.ID, schema.ID != "")
	put("$anchor", schema.Anchor, schema.Anchor != "")
	put("$ref", schema.Ref, schema.Ref != "")
	put("$defs", schema.Defs, len(schema.Defs) != 0)
	put("readOnly", schema.ReadOnly, schema.ReadOnly != nil)
	put("writeOnly", schema.WriteOnly, schema.WriteOnly != nil)
	put("deprecated", schema.Deprecated, schema.Deprecated != nil)
	return json.Marshal(m)
}

func (value ToolSchemaAdditionalProperties) MarshalJSON() ([]byte, error) {
	if (value.Allowed == nil) == (value.Schema == nil) {
		return nil, errors.New("additionalProperties must set exactly one of Allowed or Schema")
	}
	if value.Allowed != nil {
		return json.Marshal(*value.Allowed)
	}
	return json.Marshal(value.Schema)
}

func (schema ToolSchema) rejectMixedRaw() error {
	copy := schema
	copy.raw = nil
	encoded, err := copy.MarshalJSON()
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, []byte("{}")) {
		return errors.New("raw schema cannot be mixed with typed fields")
	}
	return nil
}
func isJSONObject(raw []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}
func validateAdditionalProperties(value *ToolSchemaAdditionalProperties) error {
	if value == nil {
		return nil
	}
	_, err := value.MarshalJSON()
	return err
}
func cloneRawValues(values []json.RawMessage) []json.RawMessage {
	if values == nil {
		return nil
	}
	out := make([]json.RawMessage, len(values))
	for i := range values {
		out[i] = cloneRawJSON(values[i])
	}
	return out
}
func cloneSchemas(values []ToolSchema) []ToolSchema {
	if values == nil {
		return nil
	}
	out := make([]ToolSchema, len(values))
	for i := range values {
		out[i] = values[i].Clone()
	}
	return out
}
func cloneSchemaMap(values map[string]ToolSchema) map[string]ToolSchema {
	if values == nil {
		return nil
	}
	out := make(map[string]ToolSchema, len(values))
	for k, v := range values {
		out[k] = v.Clone()
	}
	return out
}
func cloneSchemaPtr(value *ToolSchema) *ToolSchema {
	if value == nil {
		return nil
	}
	clone := value.Clone()
	return &clone
}
func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
func cloneAdditionalProperties(value *ToolSchemaAdditionalProperties) *ToolSchemaAdditionalProperties {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Allowed = cloneBool(value.Allowed)
	clone.Schema = cloneSchemaPtr(value.Schema)
	return &clone
}
