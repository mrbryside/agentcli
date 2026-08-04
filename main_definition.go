package agentcli

import (
	"fmt"
	"strings"
)

func parseMainDefinition(path string, contents []byte) (agentDefinition, error) {
	var metadata struct {
		Provider string   `yaml:"provider"`
		Model    string   `yaml:"model"`
		Skills   []string `yaml:"skills"`
		Tools    []string `yaml:"tools"`
	}
	instructions, err := parseDefinitionDocument("main agent", path, contents, &metadata)
	if err != nil {
		return agentDefinition{}, err
	}
	metadata.Provider = strings.TrimSpace(metadata.Provider)
	metadata.Model = strings.TrimSpace(metadata.Model)
	if metadata.Provider == "" || metadata.Model == "" {
		return agentDefinition{}, fmt.Errorf("main agent %s: provider and model are required", path)
	}
	metadata.Skills, metadata.Tools, err = normalizeDefinitionCapabilities("main agent", path, metadata.Skills, metadata.Tools)
	if err != nil {
		return agentDefinition{}, err
	}
	for _, toolName := range metadata.Tools {
		if toolName == taskToolName {
			return agentDefinition{}, fmt.Errorf("main agent %s: task is a framework tool and must not be listed in tools", path)
		}
	}
	return agentDefinition{
		Name: "main", Provider: metadata.Provider, Model: metadata.Model,
		Skills: metadata.Skills, Tools: metadata.Tools, Instructions: instructions, Path: path,
	}, nil
}
