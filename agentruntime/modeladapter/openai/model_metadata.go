package openai

import (
	"fmt"
	"strings"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
)

// ModelMetadataResolver resolves provider-neutral limits for an OpenAI model
// identifier. It lets OpenAI-compatible deployments supply their own catalog
// without exposing provider SDK types to AgentRuntime.
type ModelMetadataResolver func(model string) (agentruntime.ModelMetadata, error)

var _ agentruntime.ModelMetadataProvider = (*Adapter)(nil)

// ModelMetadata returns known limits for the configured model. It never
// guesses for arbitrary OpenAI-compatible model IDs: callers that need
// metadata must configure a resolver for models outside the built-in catalog.
func (a *Adapter) ModelMetadata() (agentruntime.ModelMetadata, error) {
	if a == nil {
		return agentruntime.ModelMetadata{}, fmt.Errorf("OpenAI model adapter is nil")
	}
	model := strings.TrimSpace(a.config.Model)
	if model == "" {
		return agentruntime.ModelMetadata{}, fmt.Errorf("OpenAI model is required")
	}

	resolver := a.config.MetadataResolver
	if resolver == nil {
		resolver = defaultModelMetadata
	}
	metadata, err := resolver(model)
	if err != nil {
		return agentruntime.ModelMetadata{}, fmt.Errorf("resolve OpenAI model metadata for %q: %w", model, err)
	}
	if err := metadata.Validate(); err != nil {
		return agentruntime.ModelMetadata{}, fmt.Errorf("resolve OpenAI model metadata for %q: %w", model, err)
	}
	return metadata, nil
}

func defaultModelMetadata(model string) (agentruntime.ModelMetadata, error) {
	// Keep this catalog deliberately small and conservative. Only exact known
	// aliases and YYYY-MM-DD version suffixes resolve here; a generic family
	// prefix can accidentally classify an unrelated compatible model.
	for _, alias := range knownModelAliases {
		if model == alias.name || datedModelAlias(alias.name, model) {
			return alias.metadata, nil
		}
	}
	return agentruntime.ModelMetadata{}, fmt.Errorf("model limits are unknown; configure Config.MetadataResolver")
}

func datedModelAlias(alias, model string) bool {
	if !strings.HasPrefix(model, alias+"-") {
		return false
	}
	_, err := time.Parse("2006-01-02", strings.TrimPrefix(model, alias+"-"))
	return err == nil
}

var knownModelAliases = []modelAlias{
	{name: "gpt-4.1", metadata: agentruntime.ModelMetadata{ContextWindowTokens: 1_047_576, MaxOutputTokens: 32_768}},
	{name: "gpt-4.1-mini", metadata: agentruntime.ModelMetadata{ContextWindowTokens: 1_047_576, MaxOutputTokens: 32_768}},
	{name: "gpt-4.1-nano", metadata: agentruntime.ModelMetadata{ContextWindowTokens: 1_047_576, MaxOutputTokens: 32_768}},
	{name: "gpt-4o", metadata: agentruntime.ModelMetadata{ContextWindowTokens: 128_000, MaxOutputTokens: 16_384}},
	{name: "gpt-4o-mini", metadata: agentruntime.ModelMetadata{ContextWindowTokens: 128_000, MaxOutputTokens: 16_384}},
	{name: "gpt-4-turbo", metadata: agentruntime.ModelMetadata{ContextWindowTokens: 128_000, MaxOutputTokens: 4_096}},
	{name: "gpt-4-turbo-preview", metadata: agentruntime.ModelMetadata{ContextWindowTokens: 128_000, MaxOutputTokens: 4_096}},
	{name: "gpt-4", metadata: agentruntime.ModelMetadata{ContextWindowTokens: 8_192, MaxOutputTokens: 8_192}},
	{name: "gpt-3.5-turbo", metadata: agentruntime.ModelMetadata{ContextWindowTokens: 16_385, MaxOutputTokens: 4_096}},
}

type modelAlias struct {
	name     string
	metadata agentruntime.ModelMetadata
}
