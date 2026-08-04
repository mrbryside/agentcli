package agentcli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithLangfuseStoresCodeConfiguration(t *testing.T) {
	input := LangfuseConfig{
		BaseURL:     "https://us.cloud.langfuse.com",
		PublicKey:   "pk-test",
		SecretKey:   "sk-test",
		Environment: "testing",
		ServiceName: "agentcli-test",
		Release:     "v1.2.3",
		SampleRate:  0.25,
		Capture: LangfuseCaptureConfig{
			Input: true,
		},
	}
	configuration := defaultConfig(t.TempDir())
	if err := WithLangfuse(input)(&configuration); err != nil {
		t.Fatal(err)
	}
	input.PublicKey = "mutated"
	if configuration.langfuseConfig == nil || configuration.langfuseConfig.PublicKey != "pk-test" ||
		configuration.langfuseConfig.SampleRate != 0.25 || !configuration.langfuseConfig.Capture.Input {
		t.Fatalf("stored Langfuse config = %#v", configuration.langfuseConfig)
	}
}

func TestWithLangfuseCreatesAgentOwnedClientAndWrapsModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	model := &scriptedModel{}
	agent, err := New(context.Background(),
		WithModel(model),
		WithLangfuse(LangfuseConfig{
			BaseURL:    server.URL,
			PublicKey:  "pk-test",
			SecretKey:  "sk-test",
			SampleRate: 1,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if agent.langfuse == nil || !agent.ownsLangfuse {
		t.Fatalf("Langfuse ownership = client %v, owned %v", agent.langfuse, agent.ownsLangfuse)
	}
	if agent.model == model {
		t.Fatal("main model was not decorated for Langfuse")
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWithLangfuseValidationRunsDuringAgentConstruction(t *testing.T) {
	agent, err := New(context.Background(),
		WithModel(&scriptedModel{}),
		WithLangfuse(LangfuseConfig{}),
	)
	if agent != nil {
		_ = agent.Close()
		t.Fatal("New returned an Agent for invalid Langfuse configuration")
	}
	if err == nil || !strings.Contains(err.Error(), "Langfuse public key is required") {
		t.Fatalf("New Langfuse error = %v", err)
	}
}
