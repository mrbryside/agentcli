package agentcli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentDefinition is the normalized definition domain shared by the main
// agent and project-defined child agents. Main fixes Name to "main" and leaves
// Description empty because neither field is configured in MAIN.md.
type AgentDefinition struct {
	Name         string
	Description  string
	Provider     string
	Model        string
	Skills       []string
	Tools        []string
	Instructions string
	Path         string
}

// SubagentDefinition is retained as the child-agent API name.
type SubagentDefinition = AgentDefinition

func loadSubagentDefinitions(root string, providers map[string]ProviderConfig, skills map[string]Skill) (map[string]SubagentDefinition, error) {
	definitions := make(map[string]SubagentDefinition)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return definitions, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read subagent directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), entry.Name()+".md")
		contents, err := readProjectFile(path, true)
		if err != nil {
			return nil, fmt.Errorf("load subagent %q: %w", entry.Name(), err)
		}
		definition, err := parseSubagentDefinition(path, contents)
		if err != nil {
			return nil, err
		}
		if definition.Name != entry.Name() {
			return nil, fmt.Errorf("subagent %s: name %q must match directory %q", path, definition.Name, entry.Name())
		}
		if _, found := providers[definition.Provider]; !found {
			return nil, fmt.Errorf("subagent %s: provider %q is not configured", path, definition.Provider)
		}
		for _, skillName := range definition.Skills {
			if _, found := skills[skillName]; !found {
				return nil, fmt.Errorf("subagent %s: skill %q is not available", path, skillName)
			}
		}
		if _, duplicate := definitions[definition.Name]; duplicate {
			return nil, fmt.Errorf("duplicate subagent name %q", definition.Name)
		}
		definitions[definition.Name] = definition
	}
	return definitions, nil
}

func parseSubagentDefinition(path string, contents []byte) (SubagentDefinition, error) {
	return parseAgentDefinition("subagent", path, contents)
}

func parseAgentDefinition(kind, path string, contents []byte) (SubagentDefinition, error) {
	var metadata struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Provider    string   `yaml:"provider"`
		Model       string   `yaml:"model"`
		Skills      []string `yaml:"skills"`
		Tools       []string `yaml:"tools"`
	}
	instructions, err := parseDefinitionDocument(kind, path, contents, &metadata)
	if err != nil {
		return SubagentDefinition{}, err
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	metadata.Description = strings.TrimSpace(metadata.Description)
	metadata.Provider = strings.TrimSpace(metadata.Provider)
	metadata.Model = strings.TrimSpace(metadata.Model)
	metadata.Skills, metadata.Tools, err = normalizeDefinitionCapabilities(kind, path, metadata.Skills, metadata.Tools)
	if err != nil {
		return SubagentDefinition{}, err
	}
	if metadata.Name == "" || metadata.Description == "" || metadata.Provider == "" || metadata.Model == "" {
		return SubagentDefinition{}, fmt.Errorf("%s %s: name, description, provider, and model are required", kind, path)
	}
	if len(metadata.Name) > 64 || !skillNamePattern.MatchString(metadata.Name) {
		return SubagentDefinition{}, fmt.Errorf("%s %s: name must be at most 64 lowercase letters, numbers, or hyphen-separated words", kind, path)
	}
	if strings.Contains(metadata.Name, "anthropic") || strings.Contains(metadata.Name, "claude") {
		return SubagentDefinition{}, fmt.Errorf("%s %s: name contains a reserved word", kind, path)
	}
	if len(metadata.Description) > 1024 || strings.ContainsAny(metadata.Description, "<>") {
		return SubagentDefinition{}, fmt.Errorf("%s %s: description must be at most 1024 characters and cannot contain XML tags", kind, path)
	}
	return SubagentDefinition{
		Name: metadata.Name, Description: metadata.Description, Provider: metadata.Provider,
		Model: metadata.Model, Skills: metadata.Skills, Tools: metadata.Tools, Instructions: instructions, Path: path,
	}, nil
}

func parseDefinitionDocument(kind, path string, contents []byte, metadata any) (string, error) {
	text := strings.ReplaceAll(string(contents), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", fmt.Errorf("%s %s: YAML front matter must start with ---", kind, path)
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return "", fmt.Errorf("%s %s: YAML front matter is not closed", kind, path)
	}
	end += 4
	if err := decodeYAML([]byte(text[4:end]), metadata); err != nil {
		return "", fmt.Errorf("%s %s metadata: %w", kind, path, err)
	}
	instructions := strings.TrimSpace(text[end+5:])
	if instructions == "" {
		return "", fmt.Errorf("%s %s: Markdown instructions are required", kind, path)
	}
	return instructions, nil
}

func normalizeDefinitionCapabilities(kind, path string, skills, tools []string) ([]string, []string, error) {
	if skills != nil && len(skills) == 0 {
		return nil, nil, fmt.Errorf("%s %s: remove skills when no skills are allowed; skills: [] is not valid", kind, path)
	}
	if tools != nil && len(tools) == 0 {
		return nil, nil, fmt.Errorf("%s %s: remove tools when no tools are allowed; tools: [] is not valid", kind, path)
	}
	seenSkills := make(map[string]struct{}, len(skills))
	for index, name := range skills {
		name = strings.TrimSpace(name)
		if name == "" || !skillNamePattern.MatchString(name) {
			return nil, nil, fmt.Errorf("%s %s: skill %d must be a lowercase skill name", kind, path, index)
		}
		if _, duplicate := seenSkills[name]; duplicate {
			return nil, nil, fmt.Errorf("%s %s: duplicate skill %q", kind, path, name)
		}
		seenSkills[name] = struct{}{}
		skills[index] = name
	}
	sort.Strings(skills)
	seenTools := make(map[string]struct{}, len(tools))
	for index, name := range tools {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, nil, fmt.Errorf("%s %s: tool %d cannot be empty", kind, path, index)
		}
		if _, duplicate := seenTools[name]; duplicate {
			return nil, nil, fmt.Errorf("%s %s: duplicate tool %q", kind, path, name)
		}
		seenTools[name] = struct{}{}
		tools[index] = name
	}
	sort.Strings(tools)
	return append([]string{}, skills...), append([]string{}, tools...), nil
}

// sortedSubagentDefinitions returns metadata in stable name order.
func sortedSubagentDefinitions(definitions map[string]SubagentDefinition) []SubagentDefinition {
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]SubagentDefinition, len(names))
	for index, name := range names {
		result[index] = cloneSubagentDefinition(definitions[name])
	}
	return result
}

func cloneSubagentDefinition(definition SubagentDefinition) SubagentDefinition {
	clone := definition
	clone.Skills = append([]string{}, definition.Skills...)
	clone.Tools = append([]string{}, definition.Tools...)
	return clone
}
