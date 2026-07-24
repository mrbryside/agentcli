package agentcli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/toolexecution"
)

// Tool is the low-level custom-tool declaration. It is an alias so callers
// can register a raw JSON handler without importing an internal package.
type Tool = toolexecution.Tool

// ToolDefinition describes a callable tool.
type ToolDefinition = agentruntime.ToolDefinition

// ToolPermissionConfig describes a fixed permission requirement for a tool.
type ToolPermissionConfig = toolexecution.PermissionConfig

// PermissionAction and PermissionRisk are the capability and risk values used
// by ToolPermissionConfig.
type PermissionAction = permission.Action
type PermissionRisk = permission.Risk

const (
	FilesystemRead  = permission.FilesystemRead
	FilesystemWrite = permission.FilesystemWrite
	ProcessExecute  = permission.ProcessExecute
	NetworkAccess   = permission.NetworkAccess
	SandboxBypass   = permission.SandboxBypass
	RiskLow         = permission.RiskLow
	RiskMedium      = permission.RiskMedium
	RiskHigh        = permission.RiskHigh
)

// ToolStaticPermission creates the fixed permission descriptor used by a
// low-level Tool declaration. StaticToolPermission remains the CustomTool
// option with the same name.
func ToolStaticPermission(config ToolPermissionConfig) toolexecution.PermissionDescriptor {
	return toolexecution.StaticPermission(config)
}

// InputSchema is the complete typed JSON Schema vocabulary for tool input.
// Use it directly for advanced schemas and ObjectSchema with ToolParameter for
// the usual object-shaped tool arguments.
type InputSchema = agentruntime.ToolSchema

// InputSchemaAdditionalProperties represents a boolean-or-schema
// additionalProperties value.
type InputSchemaAdditionalProperties = agentruntime.ToolSchemaAdditionalProperties

// AdditionalPropertiesBool creates an additionalProperties boolean value.
func AdditionalPropertiesBool(allowed bool) *InputSchemaAdditionalProperties {
	return agentruntime.AdditionalPropertiesBool(allowed)
}

// AdditionalPropertiesSchema creates a schema-valued additionalProperties value.
func AdditionalPropertiesSchema(schema InputSchema) *InputSchemaAdditionalProperties {
	return agentruntime.AdditionalPropertiesSchema(schema)
}

// RawInputSchema is the explicit escape hatch for a valid object JSON Schema
// that is not convenient to construct with InputSchema.
func RawInputSchema(raw json.RawMessage) (InputSchema, error) {
	return agentruntime.RawToolSchema(raw)
}

// ToolParameter describes one field of an ObjectSchema. Description belongs
// to the parameter declaration, while Schema exposes every JSON Schema
// feature for advanced cases.
type ToolParameter struct {
	Schema      InputSchema
	Description string
	Mandatory   bool
}

// Parameter attaches a description to any InputSchema.
func Parameter(schema InputSchema, description string) ToolParameter {
	return ToolParameter{Schema: schema, Description: description}
}

func StringParameter(description string) ToolParameter {
	return Parameter(InputSchema{Type: "string"}, description)
}
func IntegerParameter(description string) ToolParameter {
	return Parameter(InputSchema{Type: "integer"}, description)
}
func NumberParameter(description string) ToolParameter {
	return Parameter(InputSchema{Type: "number"}, description)
}
func BooleanParameter(description string) ToolParameter {
	return Parameter(InputSchema{Type: "boolean"}, description)
}
func NullParameter(description string) ToolParameter {
	return Parameter(InputSchema{Type: "null"}, description)
}
func ObjectParameter(description string, schema InputSchema) ToolParameter {
	return Parameter(schema, description)
}
func ArrayParameter(description string, items InputSchema) ToolParameter {
	return Parameter(InputSchema{Type: "array", Items: &items}, description)
}

// Required marks this parameter as required in its parent object schema.
func (parameter ToolParameter) Required() ToolParameter {
	parameter.Mandatory = true
	return parameter
}

// Optional marks this parameter as optional in its parent object schema.
func (parameter ToolParameter) Optional() ToolParameter {
	parameter.Mandatory = false
	return parameter
}

// WithDescription replaces this parameter's description.
func (parameter ToolParameter) WithDescription(description string) ToolParameter {
	parameter.Description = description
	return parameter
}

func (parameter ToolParameter) MinLength(value int) ToolParameter {
	parameter.Schema.MinLength = json.Number(fmt.Sprint(value))
	return parameter
}
func (parameter ToolParameter) MaxLength(value int) ToolParameter {
	parameter.Schema.MaxLength = json.Number(fmt.Sprint(value))
	return parameter
}
func (parameter ToolParameter) Pattern(value string) ToolParameter {
	parameter.Schema.Pattern = value
	return parameter
}
func (parameter ToolParameter) Format(value string) ToolParameter {
	parameter.Schema.Format = value
	return parameter
}
func (parameter ToolParameter) Minimum(value any) ToolParameter {
	parameter.Schema.Minimum = toolSchemaNumber(value)
	return parameter
}
func (parameter ToolParameter) Maximum(value any) ToolParameter {
	parameter.Schema.Maximum = toolSchemaNumber(value)
	return parameter
}
func (parameter ToolParameter) Items(value InputSchema) ToolParameter {
	parameter.Schema.Items = &value
	return parameter
}

func toolSchemaNumber(value any) json.Number {
	switch number := value.(type) {
	case json.Number:
		return number
	case string:
		return json.Number(number)
	case int:
		return json.Number(fmt.Sprint(number))
	case int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return json.Number(fmt.Sprint(number))
	default:
		panic(fmt.Sprintf("agentcli.ToolParameter numeric value must be a number, got %T", value))
	}
}

// ObjectSchema builds an object schema from an exported struct whose fields
// are ToolParameter. Field names become lower_snake_case unless a json tag
// supplies the name. Invalid declarations panic; use TryObjectSchema when an
// error must be handled at runtime.
func ObjectSchema(parameters any) InputSchema {
	schema, err := TryObjectSchema(parameters)
	if err != nil {
		panic(fmt.Sprintf("agentcli.ObjectSchema: %v", err))
	}
	return schema
}

// TryObjectSchema is the error-returning form of ObjectSchema.
func TryObjectSchema(parameters any) (InputSchema, error) {
	value := reflect.ValueOf(parameters)
	if !value.IsValid() {
		return InputSchema{}, fmt.Errorf("parameters must be a struct, got nil")
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return InputSchema{}, fmt.Errorf("parameters must not be a nil pointer")
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return InputSchema{}, fmt.Errorf("parameters must be a struct, got %s", value.Kind())
	}
	typeOf := value.Type()
	properties := make(map[string]InputSchema)
	var required []string
	parameterType := reflect.TypeFor[ToolParameter]()
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if field.PkgPath != "" {
			continue
		}
		if field.Type != parameterType {
			return InputSchema{}, fmt.Errorf("field %s must have type agentcli.ToolParameter", field.Name)
		}
		name, include := toolParameterName(field)
		if !include {
			continue
		}
		if _, exists := properties[name]; exists {
			return InputSchema{}, fmt.Errorf("duplicate parameter name %q", name)
		}
		parameter := value.Field(index).Interface().(ToolParameter)
		schema := parameter.Schema.Clone()
		if parameter.Description != "" {
			if schema.Description != "" && schema.Description != parameter.Description {
				return InputSchema{}, fmt.Errorf("field %s has conflicting descriptions", field.Name)
			}
			schema.Description = parameter.Description
		}
		properties[name] = schema
		if parameter.Mandatory {
			required = append(required, name)
		}
	}
	return InputSchema{Type: "object", Properties: properties, Required: required, AdditionalProperties: AdditionalPropertiesBool(false)}, nil
}

func toolParameterName(field reflect.StructField) (string, bool) {
	if tag, exists := field.Tag.Lookup("json"); exists {
		parts := strings.Split(tag, ",")
		if parts[0] == "-" {
			return "", false
		}
		if parts[0] != "" {
			return parts[0], true
		}
	}
	var builder strings.Builder
	for index, character := range field.Name {
		if index > 0 && unicode.IsUpper(character) {
			builder.WriteByte('_')
		}
		builder.WriteRune(unicode.ToLower(character))
	}
	return builder.String(), true
}
