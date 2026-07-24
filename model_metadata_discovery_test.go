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

func TestLoadProjectUsesConfiguredProviderModelMetadataWithoutDiscovery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `compaction:
  provider: private
  model: compact-model
providers:
  private:
    type: openai
    url: `+server.URL+`/v1
    api_key: test-key
    models:
      gpt-test:
        context_window_tokens: 131072
        max_output_tokens: 16384
      compact-model:
        context_window_tokens: 65536
        max_output_tokens: 8192
`)
	writeMainAgentDefinition(t, root, "private", "gpt-test", "skills: [reviewing-go, testing-go]")

	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("metadata discovery requests = %d; want 0", requests.Load())
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
	if metadata != (agentruntime.ModelMetadata{ContextWindowTokens: 131072, MaxOutputTokens: 16384}) {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestConfiguredProviderModelMetadataValidation(t *testing.T) {
	tests := []struct {
		name   string
		models map[string]ModelMetadataConfig
		want   string
	}{
		{
			name:   "missing context",
			models: map[string]ModelMetadataConfig{"model": {MaxOutputTokens: 1024}},
			want:   "context window tokens must be positive",
		},
		{
			name: "output exceeds context",
			models: map[string]ModelMetadataConfig{
				"model": {ContextWindowTokens: 1024, MaxOutputTokens: 2048},
			},
			want: "maximum output tokens cannot exceed",
		},
		{
			name: "case insensitive duplicate",
			models: map[string]ModelMetadataConfig{
				"Model": {ContextWindowTokens: 1024},
				"model": {ContextWindowTokens: 1024},
			},
			want: "differ only by case",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateConfiguredModelMetadata("private", test.models)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestUnresolvedModelMetadataErrorIncludesConfigExample(t *testing.T) {
	err := unresolvedModelMetadataError(
		projectModelReference{provider: "private", model: "unknown-model"},
		fmt.Errorf("not found"),
	)
	for _, expected := range []string{
		`provider "private" model "unknown-model"`,
		"providers:",
		`"private":`,
		`"unknown-model":`,
		"context_window_tokens:",
		"max_output_tokens:",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error = %q; missing %q", err, expected)
		}
	}
}
