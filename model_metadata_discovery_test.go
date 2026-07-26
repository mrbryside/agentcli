package agentcli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
)

func TestDiscoverModelMetadataUsesProviderBeforeModelsDev(t *testing.T) {
	var authorization string
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"data":[{"id":"private-model","context_window_tokens":131072,"max_output_tokens":16384}]}`)
	}))
	defer providerServer.Close()

	var modelsDevRequests atomic.Int32
	modelsDevServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		modelsDevRequests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{}`)
	}))
	defer modelsDevServer.Close()

	metadata, err := discoverModelMetadata(
		context.Background(),
		http.DefaultClient,
		ProviderConfig{URL: providerServer.URL + "/v1", APIKey: "test-key"},
		"private",
		"private-model",
		modelsDevServer.URL,
		defaultProjectModelMetadata(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if metadata != (agentruntime.ModelMetadata{ContextWindowTokens: 131072, MaxOutputTokens: 16384}) {
		t.Fatalf("metadata = %#v", metadata)
	}
	if authorization != "Bearer test-key" {
		t.Fatalf("authorization = %q", authorization)
	}
	if modelsDevRequests.Load() != 0 {
		t.Fatalf("models.dev requests = %d; want 0 when provider metadata is valid", modelsDevRequests.Load())
	}
}

func TestDiscoverModelMetadataFallsBackToModelsDev(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"data":[{"id":"private-model","object":"model"}]}`)
	}))
	defer providerServer.Close()
	modelsDevServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{
			"catalog-provider": {
				"models": {
					"private-model": {
						"limit": {"context": 262144, "output": 32768}
					}
				}
			}
		}`)
	}))
	defer modelsDevServer.Close()

	metadata, err := discoverModelMetadata(
		context.Background(),
		http.DefaultClient,
		ProviderConfig{URL: providerServer.URL + "/v1"},
		"private",
		"private-model",
		modelsDevServer.URL,
		defaultProjectModelMetadata(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if metadata != (agentruntime.ModelMetadata{ContextWindowTokens: 262144, MaxOutputTokens: 32768}) {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestFetchModelsDevMetadataPrefersProviderTypeOverGlobalMatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{
			"openai": {
				"models": {
					"shared-model": {"limit": {"context": 128000, "output": 16000}}
				}
			},
			"other": {
				"models": {
					"shared-model": {"limit": {"context": 64000, "output": 8000}}
				}
			}
		}`)
	}))
	defer server.Close()

	metadata, err := fetchModelsDevMetadata(
		context.Background(),
		http.DefaultClient,
		server.URL,
		"primary",
		ProviderTypeOpenAI,
		"shared-model",
	)
	if err != nil {
		t.Fatal(err)
	}
	if metadata != (agentruntime.ModelMetadata{ContextWindowTokens: 128000, MaxOutputTokens: 16000}) {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestLoadProjectUsesIndependentProviderMetadata(t *testing.T) {
	var mainRequests atomic.Int32
	mainServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mainRequests.Add(1)
	}))
	defer mainServer.Close()
	var compactionRequests atomic.Int32
	compactionServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		compactionRequests.Add(1)
	}))
	defer compactionServer.Close()

	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `compaction:
  provider: compact
  model: compact-model
providers:
  main:
    type: openai
    url: `+mainServer.URL+`/v1
    api_key: test-key
    models:
      gpt-test:
        context_window_tokens: 262144
        max_output_tokens: 32768
  compact:
    type: openai
    url: `+compactionServer.URL+`/v1
    api_key: test-key
    models:
      compact-model:
        context_window_tokens: 131072
        max_output_tokens: 16384
`)
	writeMainAgentDefinition(t, root, "main", "gpt-test", "skills: [reviewing-go, testing-go]")

	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if mainRequests.Load() != 0 || compactionRequests.Load() != 0 {
		t.Fatalf("metadata discovery requests: main=%d compaction=%d; want configured providers to skip discovery", mainRequests.Load(), compactionRequests.Load())
	}
	model, err := project.Model()
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := model.(agentruntime.ModelMetadataProvider)
	if !ok {
		t.Fatal("configured model does not expose metadata")
	}
	metadata, err := provider.ModelMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if metadata != (agentruntime.ModelMetadata{ContextWindowTokens: 262144, MaxOutputTokens: 32768}) {
		t.Fatalf("metadata = %#v", metadata)
	}
	compactionModel, err := project.CompactionModel()
	if err != nil {
		t.Fatal(err)
	}
	compactionProvider, ok := compactionModel.(agentruntime.ModelMetadataProvider)
	if !ok {
		t.Fatal("configured compaction model does not expose metadata")
	}
	compactionMetadata, err := compactionProvider.ModelMetadata()
	if err != nil {
		t.Fatal(err)
	}
	wantCompaction := agentruntime.ModelMetadata{ContextWindowTokens: 131072, MaxOutputTokens: 16384}
	if compactionMetadata != wantCompaction {
		t.Fatalf("compaction metadata = %#v; want %#v", compactionMetadata, wantCompaction)
	}
}

func TestProjectModelUsesProviderMetadataWithoutCompaction(t *testing.T) {
	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `providers:
  main:
    type: openai
    api_key: test-key
    models:
      custom-model:
        context_window_tokens: 196608
        max_output_tokens: 24576
`)
	writeMainAgentDefinition(t, root, "main", "custom-model", "skills: [reviewing-go, testing-go]")

	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	model, err := project.Model()
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := model.(agentruntime.ModelMetadataProvider)
	if !ok {
		t.Fatal("configured model does not expose metadata")
	}
	metadata, err := provider.ModelMetadata()
	if err != nil {
		t.Fatal(err)
	}
	want := agentruntime.ModelMetadata{ContextWindowTokens: 196608, MaxOutputTokens: 24576}
	if metadata != want {
		t.Fatalf("metadata = %#v; want %#v", metadata, want)
	}
}

func TestConfiguredProviderMetadataValidation(t *testing.T) {
	tests := []struct {
		name   string
		config ProviderConfig
		want   string
	}{
		{
			name:   "not configured",
			config: ProviderConfig{},
		},
		{
			name: "missing context",
			config: ProviderConfig{Models: map[string]ProviderModelConfig{
				"selected": {MaxOutputTokens: 1024},
			}},
			want: "context window tokens must be positive",
		},
		{
			name: "output exceeds context",
			config: ProviderConfig{Models: map[string]ProviderModelConfig{
				"selected": {ContextWindowTokens: 1024, MaxOutputTokens: 2048},
			}},
			want: "maximum output tokens cannot exceed",
		},
		{
			name: "valid",
			config: ProviderConfig{Models: map[string]ProviderModelConfig{
				"selected": {ContextWindowTokens: 122880, MaxOutputTokens: 66560},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, configured, err := configuredProviderMetadata("test", "selected", test.config)
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error = %v; want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantConfigured := test.name == "valid"
			if configured != wantConfigured {
				t.Fatalf("configured = %t; want %t", configured, wantConfigured)
			}
		})
	}
}

func TestConfiguredProviderMetadataMatchesExactModelName(t *testing.T) {
	config := ProviderConfig{Models: map[string]ProviderModelConfig{
		"selected": {
			ContextWindowTokens: 122880,
			MaxOutputTokens:     66560,
		},
	}}

	if _, configured, err := configuredProviderMetadata("test", "other", config); err != nil {
		t.Fatal(err)
	} else if configured {
		t.Fatal("unlisted model unexpectedly received configured metadata")
	}
}

func TestDiscoverModelMetadataUsesExactDefaultsWhenSourcesFail(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	defer providerServer.Close()
	modelsDevServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{}`)
	}))
	defer modelsDevServer.Close()

	metadata, err := discoverModelMetadata(
		context.Background(),
		http.DefaultClient,
		ProviderConfig{URL: providerServer.URL + "/v1"},
		"private",
		"unknown-model",
		modelsDevServer.URL,
		defaultProjectModelMetadata(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if metadata != (agentruntime.ModelMetadata{ContextWindowTokens: 122880, MaxOutputTokens: 66560}) {
		t.Fatalf("metadata = %#v", metadata)
	}
}
