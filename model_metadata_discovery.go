package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
)

const (
	defaultModelMetadataDiscoveryTimeout = 10 * time.Second
	maximumModelMetadataResponseBytes    = 8 << 20
	modelsDevAPIURL                      = "https://models.dev/api.json"
)

type projectModelReference struct {
	provider string
	model    string
}

type resolvedProjectModelMetadata struct {
	reference projectModelReference
	metadata  agentruntime.ModelMetadata
	err       error
}

type providerModelsResponse struct {
	Data []providerModelRecord `json:"data"`
}

type providerModelRecord struct {
	ID                  string `json:"id"`
	ContextWindowTokens int    `json:"context_window_tokens"`
	ContextWindow       int    `json:"context_window"`
	ContextLength       int    `json:"context_length"`
	MaxOutputTokens     int    `json:"max_output_tokens"`
	MaxCompletionTokens int    `json:"max_completion_tokens"`
	Limit               struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
}

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
}

func (project *Project) resolveRequiredModelMetadata(ctx context.Context) error {
	references := project.requiredModelMetadataReferences()
	if len(references) == 0 {
		return nil
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, defaultModelMetadataDiscoveryTimeout)
	defer cancel()

	results := make(chan resolvedProjectModelMetadata, len(references))
	for _, reference := range references {
		reference := reference
		go func() {
			metadata, err := project.resolveModelMetadata(discoveryCtx, reference)
			results <- resolvedProjectModelMetadata{reference: reference, metadata: metadata, err: err}
		}()
	}

	resolved := make([]resolvedProjectModelMetadata, 0, len(references))
	for range references {
		result := <-results
		if result.err != nil {
			cancel()
			return result.err
		}
		resolved = append(resolved, result)
	}
	for _, result := range resolved {
		providerConfig := project.config.Providers[result.reference.provider]
		if providerConfig.Models == nil {
			providerConfig.Models = make(map[string]ModelMetadataConfig)
		}
		providerConfig.Models[result.reference.model] = ModelMetadataConfig{
			ContextWindowTokens: result.metadata.ContextWindowTokens,
			MaxOutputTokens:     result.metadata.MaxOutputTokens,
		}
		project.config.Providers[result.reference.provider] = providerConfig
	}
	return nil
}

func (project *Project) requiredModelMetadataReferences() []projectModelReference {
	unique := make(map[projectModelReference]struct{})
	add := func(providerName, modelName string) {
		reference := projectModelReference{
			provider: strings.TrimSpace(providerName),
			model:    strings.TrimSpace(modelName),
		}
		if reference.provider != "" && reference.model != "" {
			unique[reference] = struct{}{}
		}
	}

	add(project.providerName, project.modelName)
	if project.compaction != nil && project.compaction.Auto {
		add(project.compaction.Provider, project.compaction.Model)
	}
	for _, definition := range project.subagents {
		add(definition.Provider, definition.Model)
	}

	references := make([]projectModelReference, 0, len(unique))
	for reference := range unique {
		references = append(references, reference)
	}
	sort.Slice(references, func(left, right int) bool {
		if references[left].provider == references[right].provider {
			return references[left].model < references[right].model
		}
		return references[left].provider < references[right].provider
	})
	return references
}

func (project *Project) resolveModelMetadata(ctx context.Context, reference projectModelReference) (agentruntime.ModelMetadata, error) {
	providerConfig, ok := project.config.Providers[reference.provider]
	if !ok {
		return agentruntime.ModelMetadata{}, fmt.Errorf("provider %q is not configured", reference.provider)
	}
	if metadata, configured := configuredModelMetadata(providerConfig, reference.model); configured {
		return metadata, nil
	}

	metadata, err := discoverModelMetadata(ctx, http.DefaultClient, providerConfig, reference.provider, reference.model, modelsDevAPIURL)
	if err != nil {
		return agentruntime.ModelMetadata{}, unresolvedModelMetadataError(reference, err)
	}
	return metadata, nil
}

func discoverModelMetadata(
	ctx context.Context,
	client *http.Client,
	providerConfig ProviderConfig,
	providerName string,
	modelName string,
	modelsDevURL string,
) (agentruntime.ModelMetadata, error) {
	if client == nil {
		client = http.DefaultClient
	}

	providerMetadata, providerErr := fetchProviderModelMetadata(ctx, client, providerConfig, modelName)
	if providerErr == nil {
		if err := providerMetadata.Validate(); err == nil {
			return providerMetadata, nil
		} else {
			providerErr = err
		}
	}

	modelsDevMetadata, modelsDevErr := fetchModelsDevMetadata(ctx, client, modelsDevURL, providerName, providerConfig.Type, modelName)
	if modelsDevErr == nil {
		if err := modelsDevMetadata.Validate(); err == nil {
			return modelsDevMetadata, nil
		} else {
			modelsDevErr = err
		}
	}
	return agentruntime.ModelMetadata{}, errors.Join(
		fmt.Errorf("provider /models: %w", providerErr),
		fmt.Errorf("models.dev: %w", modelsDevErr),
	)
}

func fetchProviderModelMetadata(
	ctx context.Context,
	client *http.Client,
	providerConfig ProviderConfig,
	modelName string,
) (agentruntime.ModelMetadata, error) {
	endpoint, err := providerModelsEndpoint(providerConfig.URL)
	if err != nil {
		return agentruntime.ModelMetadata{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return agentruntime.ModelMetadata{}, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if providerConfig.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+providerConfig.APIKey)
	}
	var response providerModelsResponse
	if err := fetchModelMetadataJSON(client, request, &response); err != nil {
		return agentruntime.ModelMetadata{}, err
	}
	for _, record := range response.Data {
		if !strings.EqualFold(strings.TrimSpace(record.ID), strings.TrimSpace(modelName)) {
			continue
		}
		metadata := agentruntime.ModelMetadata{
			ContextWindowTokens: firstPositive(record.ContextWindowTokens, record.ContextWindow, record.ContextLength, record.Limit.Context),
			MaxOutputTokens:     firstPositive(record.MaxOutputTokens, record.MaxCompletionTokens, record.Limit.Output),
		}
		if err := metadata.Validate(); err != nil {
			return agentruntime.ModelMetadata{}, fmt.Errorf("model %q returned no valid limits: %w", modelName, err)
		}
		return metadata, nil
	}
	return agentruntime.ModelMetadata{}, fmt.Errorf("model %q was not returned", modelName)
}

func fetchModelsDevMetadata(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	providerName string,
	providerType ProviderType,
	modelName string,
) (agentruntime.ModelMetadata, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return agentruntime.ModelMetadata{}, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	var catalog map[string]modelsDevProvider
	if err := fetchModelMetadataJSON(client, request, &catalog); err != nil {
		return agentruntime.ModelMetadata{}, err
	}

	type candidate struct {
		provider string
		metadata agentruntime.ModelMetadata
	}
	candidates := make([]candidate, 0)
	for catalogProvider, provider := range catalog {
		for catalogModel, model := range provider.Models {
			if !strings.EqualFold(strings.TrimSpace(catalogModel), strings.TrimSpace(modelName)) {
				continue
			}
			metadata := agentruntime.ModelMetadata{
				ContextWindowTokens: model.Limit.Context,
				MaxOutputTokens:     model.Limit.Output,
			}
			if err := metadata.Validate(); err != nil {
				continue
			}
			if strings.EqualFold(catalogProvider, providerName) || strings.EqualFold(catalogProvider, string(providerType)) {
				return metadata, nil
			}
			candidates = append(candidates, candidate{provider: catalogProvider, metadata: metadata})
		}
	}
	if len(candidates) == 0 {
		return agentruntime.ModelMetadata{}, fmt.Errorf("model %q was not found", modelName)
	}
	first := candidates[0].metadata
	for _, candidate := range candidates[1:] {
		if candidate.metadata != first {
			providers := make([]string, 0, len(candidates))
			for _, item := range candidates {
				providers = append(providers, item.provider)
			}
			sort.Strings(providers)
			return agentruntime.ModelMetadata{}, fmt.Errorf("model %q is ambiguous across providers %s", modelName, strings.Join(providers, ", "))
		}
	}
	return first, nil
}

func providerModelsEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("provider URL %q is invalid", baseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func fetchModelMetadataJSON(client *http.Client, request *http.Request, target any) error {
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("request returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maximumModelMetadataResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(body) > maximumModelMetadataResponseBytes {
		return fmt.Errorf("response body exceeds %d bytes", maximumModelMetadataResponseBytes)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func configuredModelMetadata(providerConfig ProviderConfig, modelName string) (agentruntime.ModelMetadata, bool) {
	for configuredName, configured := range providerConfig.Models {
		if !strings.EqualFold(strings.TrimSpace(configuredName), strings.TrimSpace(modelName)) {
			continue
		}
		return agentruntime.ModelMetadata{
			ContextWindowTokens: configured.ContextWindowTokens,
			MaxOutputTokens:     configured.MaxOutputTokens,
		}, true
	}
	return agentruntime.ModelMetadata{}, false
}

func validateConfiguredModelMetadata(providerName string, models map[string]ModelMetadataConfig) error {
	normalized := make(map[string]string, len(models))
	for modelName, configured := range models {
		trimmed := strings.TrimSpace(modelName)
		if trimmed == "" {
			return fmt.Errorf("provider %q model name is required", providerName)
		}
		lower := strings.ToLower(trimmed)
		if prior, duplicate := normalized[lower]; duplicate {
			return fmt.Errorf("provider %q models %q and %q differ only by case", providerName, prior, modelName)
		}
		normalized[lower] = modelName
		metadata := agentruntime.ModelMetadata{
			ContextWindowTokens: configured.ContextWindowTokens,
			MaxOutputTokens:     configured.MaxOutputTokens,
		}
		if err := metadata.Validate(); err != nil {
			return fmt.Errorf("provider %q model %q metadata: %w", providerName, modelName, err)
		}
	}
	return nil
}

func unresolvedModelMetadataError(reference projectModelReference, cause error) error {
	return fmt.Errorf(
		"model metadata is unavailable for provider %q model %q: %w; add explicit metadata to .agentcli/config.yaml:\nproviders:\n  %q:\n    models:\n      %q:\n        context_window_tokens: 262144\n        max_output_tokens: 65536",
		reference.provider,
		reference.model,
		cause,
		reference.provider,
		reference.model,
	)
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
