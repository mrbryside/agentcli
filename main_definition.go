package agentcli

import (
	"fmt"
	"strings"
)

func parseMainDefinition(path string, contents []byte) (AgentDefinition, error) {
	var metadata struct {
		Provider string   `yaml:"provider"`
		Model    string   `yaml:"model"`
		Skills   []string `yaml:"skills"`
		Tools    []string `yaml:"tools"`
	}
	instructions, err := parseDefinitionDocument("main agent", path, contents, &metadata)
	if err != nil {
		return AgentDefinition{}, err
	}
	metadata.Provider = strings.TrimSpace(metadata.Provider)
	metadata.Model = strings.TrimSpace(metadata.Model)
	if metadata.Provider == "" || metadata.Model == "" {
		return AgentDefinition{}, fmt.Errorf("main agent %s: provider and model are required", path)
	}
	metadata.Skills, metadata.Tools, err = normalizeDefinitionCapabilities("main agent", path, metadata.Skills, metadata.Tools)
	if err != nil {
		return AgentDefinition{}, err
	}
	return AgentDefinition{
		Name: "main", Provider: metadata.Provider, Model: metadata.Model,
		Skills: metadata.Skills, Tools: metadata.Tools, Instructions: instructions, Path: path,
	}, nil
}
