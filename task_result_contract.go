package agentcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// agentResultContract declares the one final-response object an agent must
// produce when its host needs application-only metadata. Agents without a
// contract continue to return ordinary final text.
type agentResultContract struct {
	MessageField string                              `yaml:"message_field"`
	Metadata     map[string]agentResultMetadataField `yaml:"metadata"`
}

// agentResultMetadataField describes one application-only field in a final
// result contract. Type is either "boolean" or "string".
type agentResultMetadataField struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
}

func normalizeAgentResultContract(kind, path string, contract *agentResultContract) (*agentResultContract, error) {
	if contract == nil {
		return nil, nil
	}
	clone := *contract
	clone.MessageField = strings.TrimSpace(clone.MessageField)
	if clone.MessageField == "" {
		return nil, fmt.Errorf("%s %s: result.message_field is required", kind, path)
	}
	clone.Metadata = make(map[string]agentResultMetadataField, len(contract.Metadata))
	for rawName, rawField := range contract.Metadata {
		name := strings.TrimSpace(rawName)
		field := rawField
		field.Type = strings.TrimSpace(field.Type)
		if name == "" {
			return nil, fmt.Errorf("%s %s: result.metadata field name is required", kind, path)
		}
		if field.Type != "boolean" && field.Type != "string" {
			return nil, fmt.Errorf("%s %s: result.metadata.%s.type must be boolean or string", kind, path, name)
		}
		if name == clone.MessageField {
			return nil, fmt.Errorf("%s %s: result metadata field %q conflicts with message_field", kind, path, name)
		}
		if _, duplicate := clone.Metadata[name]; duplicate {
			return nil, fmt.Errorf("%s %s: duplicate result metadata field %q", kind, path, name)
		}
		clone.Metadata[name] = field
	}
	return &clone, nil
}

func cloneAgentResultContract(contract *agentResultContract) *agentResultContract {
	if contract == nil {
		return nil
	}
	clone := *contract
	clone.Metadata = make(map[string]agentResultMetadataField, len(contract.Metadata))
	for name, field := range contract.Metadata {
		clone.Metadata[name] = field
	}
	return &clone
}

type taskFinalResult struct {
	Output   string
	Metadata map[string]any
}

func parseTaskFinalResult(definition agentDefinition, text string) (taskFinalResult, error) {
	if definition.Result == nil {
		return taskFinalResult{Output: text}, nil
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(&fields); err != nil {
		return taskFinalResult{}, fmt.Errorf("parse final result JSON: %w", err)
	}
	if fields == nil {
		return taskFinalResult{}, fmt.Errorf("parse final result JSON: expected an object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errorsIsEOF(err) {
		if err == nil {
			return taskFinalResult{}, fmt.Errorf("parse final result JSON: expected one object")
		}
		return taskFinalResult{}, fmt.Errorf("parse final result JSON: %w", err)
	}
	contract := definition.Result
	messageRaw, found := fields[contract.MessageField]
	if !found {
		return taskFinalResult{}, fmt.Errorf("final result is missing message field %q", contract.MessageField)
	}
	var messageValue any
	if err := json.Unmarshal(messageRaw, &messageValue); err != nil {
		return taskFinalResult{}, fmt.Errorf("final result message field %q must be a string", contract.MessageField)
	}
	output, ok := messageValue.(string)
	if !ok {
		return taskFinalResult{}, fmt.Errorf("final result message field %q must be a string", contract.MessageField)
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return taskFinalResult{}, fmt.Errorf("final result message field %q cannot be empty", contract.MessageField)
	}
	for name := range fields {
		if name == contract.MessageField {
			continue
		}
		if _, found := contract.Metadata[name]; !found {
			return taskFinalResult{}, fmt.Errorf("final result contains unknown metadata field %q", name)
		}
	}
	metadata := make(map[string]any, len(contract.Metadata))
	names := make([]string, 0, len(contract.Metadata))
	for name := range contract.Metadata {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		field := contract.Metadata[name]
		raw, found := fields[name]
		if !found {
			if field.Required {
				return taskFinalResult{}, fmt.Errorf("final result is missing required metadata field %q", name)
			}
			continue
		}
		value, err := parseTaskMetadataField(name, field, raw)
		if err != nil {
			return taskFinalResult{}, err
		}
		metadata[name] = value
	}
	return taskFinalResult{Output: output, Metadata: metadata}, nil
}

func parseTaskMetadataField(name string, field agentResultMetadataField, raw json.RawMessage) (any, error) {
	switch field.Type {
	case "boolean":
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("final result metadata field %q must be a boolean", name)
		}
		value, ok := decoded.(bool)
		if !ok {
			return nil, fmt.Errorf("final result metadata field %q must be a boolean", name)
		}
		return value, nil
	case "string":
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("final result metadata field %q must be a string", name)
		}
		value, ok := decoded.(string)
		if !ok {
			return nil, fmt.Errorf("final result metadata field %q must be a string", name)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("final result metadata field %q has unsupported type %q", name, field.Type)
	}
}

func errorsIsEOF(err error) bool {
	return err == io.EOF
}

func cloneTaskMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	clone := make(map[string]any, len(metadata))
	for name, value := range metadata {
		clone[name] = cloneTaskMetadataValue(value)
	}
	return clone
}

func cloneTaskMetadataValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneTaskMetadata(value)
	case []any:
		clone := make([]any, len(value))
		for index, item := range value {
			clone[index] = cloneTaskMetadataValue(item)
		}
		return clone
	case json.RawMessage:
		return bytes.Clone(value)
	case []byte:
		return bytes.Clone(value)
	default:
		return value
	}
}
