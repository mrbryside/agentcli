package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/toolexecution"
)

// CustomToolOption configures a typed custom tool. Use ToolSchema only when
// the schema inferred from the input struct is not expressive enough.
type CustomToolOption func(*customToolConfig) error

type customToolConfig struct {
	schema               agentruntime.ToolSchema
	hasSchema            bool
	turnBehavior         toolexecution.TurnBehavior
	requiredAtTurnEnd    bool
	permission           toolexecution.PermissionDescriptor
	permissionWithPolicy toolexecution.PermissionPolicyDescriptor
	confirmation         toolexecution.ConfirmationDescriptor
}

// WithCustomTool registers a typed application tool as an Agent option. The
// JSON object schema is inferred from Input, arguments are decoded strictly,
// and Output is JSON encoded automatically.
func WithCustomTool[Input, Output any](name, description string, handler func(context.Context, Input) (Output, error), options ...CustomToolOption) Option {
	return func(configuration *config) error {
		tool, err := NewCustomTool(name, description, handler, options...)
		if err != nil {
			return err
		}
		configuration.tools = append(configuration.tools, tool)
		return nil
	}
}

// NewCustomTool constructs the low-level tool value used by WithTool. Most
// agentcli applications can use WithCustomTool directly.
func NewCustomTool[Input, Output any](name, description string, handler func(context.Context, Input) (Output, error), options ...CustomToolOption) (toolexecution.Tool, error) {
	if strings.TrimSpace(name) == "" {
		return toolexecution.Tool{}, errors.New("custom tool name is required")
	}
	if handler == nil {
		return toolexecution.Tool{}, fmt.Errorf("custom tool %q handler is required", name)
	}
	configuration := customToolConfig{}
	for index, option := range options {
		if option == nil {
			return toolexecution.Tool{}, fmt.Errorf("custom tool %q option %d is nil", name, index)
		}
		if err := option(&configuration); err != nil {
			return toolexecution.Tool{}, fmt.Errorf("custom tool %q option %d: %w", name, index, err)
		}
	}
	if configuration.requiredAtTurnEnd {
		configuration.turnBehavior = EndTurn
	}
	if !configuration.hasSchema {
		schema, err := inferCustomToolSchema(reflect.TypeFor[Input]())
		if err != nil {
			return toolexecution.Tool{}, fmt.Errorf("custom tool %q input schema: %w", name, err)
		}
		configuration.schema = schema
	}
	return toolexecution.Tool{
		Definition: agentruntime.ToolDefinition{Name: name, Description: description, InputSchema: configuration.schema},
		Handler: func(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
			input, err := decodeCustomToolInput[Input](arguments)
			if err != nil {
				return nil, err
			}
			output, err := handler(ctx, input)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(output)
			if err != nil {
				return nil, fmt.Errorf("encode tool result: %w", err)
			}
			return encoded, nil
		},
		TurnBehavior:         configuration.turnBehavior,
		RequiredAtTurnEnd:    configuration.requiredAtTurnEnd,
		Permission:           configuration.permission,
		PermissionWithPolicy: configuration.permissionWithPolicy,
		Confirmation:         configuration.confirmation,
	}, nil
}

// ToolRequiredAtTurnEnd makes this custom tool a turn finalizer. A successful
// invocation ends the turn. If the model attempts to finish without invoking
// it successfully, the runtime gives the model one restricted repair round
// containing only the missing finalizer tools.
func ToolRequiredAtTurnEnd() CustomToolOption {
	return func(configuration *customToolConfig) error {
		configuration.requiredAtTurnEnd = true
		configuration.turnBehavior = EndTurn
		return nil
	}
}

// ToolTurnBehavior controls what happens after a successful custom-tool
// result is stored. ContinueTurn is the default; EndTurn completes the current
// agent turn without another model call.
func ToolTurnBehavior(behavior toolexecution.TurnBehavior) CustomToolOption {
	return func(configuration *customToolConfig) error {
		if behavior != ContinueTurn && behavior != EndTurn {
			return fmt.Errorf("unsupported turn behavior %q", behavior)
		}
		configuration.turnBehavior = behavior
		return nil
	}
}

const (
	// ContinueTurn asks the model to consume the tool result and continue.
	ContinueTurn = toolexecution.ContinueTurn
	// EndTurn stores the successful tool result and completes the turn.
	EndTurn = toolexecution.EndTurn
)

// ToolSchema overrides automatic input-schema inference with a typed schema.
// Tool parameters must use Type: "object".
func ToolSchema(schema agentruntime.ToolSchema) CustomToolOption {
	return func(configuration *customToolConfig) error {
		if schema.Type != "object" || len(schema.TypeUnion) != 0 || len(schema.Types) != 0 {
			return errors.New("schema type must be object")
		}
		configuration.schema = schema.Clone()
		configuration.hasSchema = true
		return nil
	}
}

// RawCustomToolSchema overrides inference with an advanced JSON Schema object.
// Prefer ToolSchema for schemas expressible through the typed vocabulary.
func RawCustomToolSchema(raw json.RawMessage) CustomToolOption {
	schema, err := agentruntime.RawToolSchema(raw)
	return func(configuration *customToolConfig) error {
		if err != nil {
			return err
		}
		configuration.schema = schema
		configuration.hasSchema = true
		return nil
	}
}

// StaticToolPermission applies the same permission description to every call.
func StaticToolPermission(config toolexecution.PermissionConfig) CustomToolOption {
	return func(configuration *customToolConfig) error {
		configuration.permission = toolexecution.StaticPermission(config)
		configuration.permissionWithPolicy = nil
		return nil
	}
}

// ToolPermission derives a permission description from typed call arguments.
func ToolPermission[Input any](describe func(Input) (permission.Description, error)) CustomToolOption {
	return func(configuration *customToolConfig) error {
		if describe == nil {
			return errors.New("permission descriptor is required")
		}
		configuration.permission = func(arguments json.RawMessage) (permission.Description, error) {
			input, err := decodeCustomToolInput[Input](arguments)
			if err != nil {
				return permission.Description{}, err
			}
			return describe(input)
		}
		configuration.permissionWithPolicy = nil
		return nil
	}
}

// ToolPermissionWithPolicy derives a permission description from typed call
// arguments and the immutable policy snapshot for that request.
func ToolPermissionWithPolicy[Input any](describe func(Input, permission.Policy) (permission.Description, error)) CustomToolOption {
	return func(configuration *customToolConfig) error {
		if describe == nil {
			return errors.New("permission policy descriptor is required")
		}
		configuration.permissionWithPolicy = func(arguments json.RawMessage, policy permission.Policy) (permission.Description, error) {
			input, err := decodeCustomToolInput[Input](arguments)
			if err != nil {
				return permission.Description{}, err
			}
			return describe(input, policy)
		}
		configuration.permission = nil
		return nil
	}
}

// ToolConfirmation presents invocation-specific information and requires a
// correlated Yes answer before the typed handler executes.
func ToolConfirmation[Input any](describe func(Input) (confirmation.Description, error)) CustomToolOption {
	return func(configuration *customToolConfig) error {
		if describe == nil {
			return errors.New("confirmation descriptor is required")
		}
		configuration.confirmation = func(arguments json.RawMessage) (confirmation.Description, error) {
			input, err := decodeCustomToolInput[Input](arguments)
			if err != nil {
				return confirmation.Description{}, err
			}
			return describe(input)
		}
		return nil
	}
}

func decodeCustomToolInput[Input any](arguments json.RawMessage) (Input, error) {
	var input Input
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return input, errors.New("decode tool arguments: expected a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("decode tool arguments: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return input, errors.New("decode tool arguments: multiple JSON values")
		}
		return input, fmt.Errorf("decode tool arguments: %w", err)
	}
	return input, nil
}

func inferCustomToolSchema(input reflect.Type) (agentruntime.ToolSchema, error) {
	if input == nil {
		return agentruntime.ToolSchema{}, errors.New("input type is required")
	}
	schema, err := schemaForCustomToolType(input, make(map[reflect.Type]bool), true)
	if err != nil {
		return agentruntime.ToolSchema{}, err
	}
	return schema, nil
}

func schemaForCustomToolType(value reflect.Type, active map[reflect.Type]bool, root bool) (agentruntime.ToolSchema, error) {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if root && value.Kind() != reflect.Struct && !(value.Kind() == reflect.Map && value.Key().Kind() == reflect.String) {
		return agentruntime.ToolSchema{}, fmt.Errorf("input must be a struct or map with string keys, got %s", value)
	}
	if value.PkgPath() == "time" && value.Name() == "Time" {
		return agentruntime.ToolSchema{Type: "string", Format: "date-time"}, nil
	}
	switch value.Kind() {
	case reflect.Struct:
		if active[value] {
			return agentruntime.ToolSchema{}, fmt.Errorf("recursive input type %s requires ToolSchema", value)
		}
		active[value] = true
		defer delete(active, value)
		properties := make(map[string]agentruntime.ToolSchema)
		required := make([]string, 0, value.NumField())
		for index := 0; index < value.NumField(); index++ {
			field := value.Field(index)
			if field.PkgPath != "" {
				continue
			}
			name, omit, optional := customToolJSONField(field)
			if omit {
				continue
			}
			fieldSchema, err := schemaForCustomToolType(field.Type, active, false)
			if err != nil {
				return agentruntime.ToolSchema{}, fmt.Errorf("field %s: %w", field.Name, err)
			}
			if err := applyCustomToolSchemaTags(&fieldSchema, field); err != nil {
				return agentruntime.ToolSchema{}, fmt.Errorf("field %s: %w", field.Name, err)
			}
			properties[name] = fieldSchema
			if !optional {
				required = append(required, name)
			}
		}
		return agentruntime.ToolSchema{Type: "object", Properties: properties, Required: required, AdditionalProperties: agentruntime.AdditionalPropertiesBool(false)}, nil
	case reflect.Map:
		if value.Key().Kind() != reflect.String {
			return agentruntime.ToolSchema{}, fmt.Errorf("map key must be string, got %s", value.Key())
		}
		items, err := schemaForCustomToolType(value.Elem(), active, false)
		if err != nil {
			return agentruntime.ToolSchema{}, err
		}
		return agentruntime.ToolSchema{Type: "object", AdditionalProperties: agentruntime.AdditionalPropertiesSchema(items)}, nil
	case reflect.Slice, reflect.Array:
		items, err := schemaForCustomToolType(value.Elem(), active, false)
		if err != nil {
			return agentruntime.ToolSchema{}, err
		}
		return agentruntime.ToolSchema{Type: "array", Items: &items}, nil
	case reflect.String:
		return agentruntime.ToolSchema{Type: "string"}, nil
	case reflect.Bool:
		return agentruntime.ToolSchema{Type: "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return agentruntime.ToolSchema{Type: "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return agentruntime.ToolSchema{Type: "number"}, nil
	case reflect.Interface:
		return agentruntime.ToolSchema{}, nil
	default:
		return agentruntime.ToolSchema{}, fmt.Errorf("unsupported input type %s", value)
	}
}

func customToolJSONField(field reflect.StructField) (name string, omit, optional bool) {
	name = field.Name
	parts := strings.Split(field.Tag.Get("json"), ",")
	if parts[0] == "-" {
		return "", true, false
	}
	if parts[0] != "" {
		name = parts[0]
	}
	for _, option := range parts[1:] {
		if option == "omitempty" || option == "omitzero" {
			optional = true
		}
	}
	return name, false, optional
}

func applyCustomToolSchemaTags(schema *agentruntime.ToolSchema, field reflect.StructField) error {
	for tag, keyword := range map[string]string{
		"description": "description",
		"format":      "format",
		"pattern":     "pattern",
	} {
		if value := strings.TrimSpace(field.Tag.Get(tag)); value != "" {
			switch keyword {
			case "description":
				schema.Description = value
			case "format":
				schema.Format = value
			case "pattern":
				schema.Pattern = value
			}
		}
	}
	if value := strings.TrimSpace(field.Tag.Get("enum")); value != "" {
		parts := strings.Split(value, ",")
		for index := range parts {
			parts[index] = strings.TrimSpace(parts[index])
		}
		values := make([]json.RawMessage, len(parts))
		for index, part := range parts {
			values[index] = json.RawMessage(strconv.Quote(part))
		}
		schema.Enum = values
	}
	for tag, keyword := range map[string]string{"minLength": "minLength", "maxLength": "maxLength"} {
		if value := strings.TrimSpace(field.Tag.Get(tag)); value != "" {
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return fmt.Errorf("%s must be an unsigned integer", tag)
			}
			if keyword == "minLength" {
				schema.MinLength = json.Number(strconv.FormatUint(parsed, 10))
			} else {
				schema.MaxLength = json.Number(strconv.FormatUint(parsed, 10))
			}
		}
	}
	for tag, keyword := range map[string]string{"minimum": "minimum", "maximum": "maximum"} {
		if value := strings.TrimSpace(field.Tag.Get(tag)); value != "" {
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("%s must be a number", tag)
			}
			encoded := json.Number(strconv.FormatFloat(parsed, 'g', -1, 64))
			if keyword == "minimum" {
				schema.Minimum = encoded
			} else {
				schema.Maximum = encoded
			}
		}
	}
	return nil
}
