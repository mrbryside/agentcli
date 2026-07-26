package langfuse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestObservedModelEmitsSessionGroupedGeneration(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tracerProvider.Shutdown(context.Background()) })
	client := &Client{
		tracer: tracerProvider.Tracer("test"),
		config: Config{
			Environment: "testing",
			Release:     "v1",
			Capture:     CaptureConfig{Input: true, Output: true},
		},
	}
	base := &testModel{
		result: provider.StreamResult{
			Content:   "answer",
			Reasoning: "private chain",
			Finished:  true,
		},
		events: []provider.StreamEvent{
			{Type: provider.ContentReceived, Content: "answer"},
			{Type: provider.StreamCompleted, FinishReason: "stop"},
		},
	}
	model := client.ObserveModel(base)
	stream, err := model.Start(context.Background(), agentruntime.ModelRequest{
		SessionID:       "session-123",
		TurnID:          "turn-456",
		SystemPrompts:   []string{"system prompt"},
		Messages:        []agentruntime.Message{{Type: agentruntime.MessageTypeUser, Content: "hello"}},
		MaxOutputTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stream == nil {
		t.Fatal("observed model returned nil stream")
	}

	span := waitForSpan(t, exporter)
	attributes := spanAttributes(span)
	for key, want := range map[string]string{
		"langfuse.observation.type":                   "generation",
		"langfuse.trace.name":                         "llm-call",
		"langfuse.session.id":                         "session-123",
		"langfuse.observation.metadata.turn_id":       "turn-456",
		"langfuse.observation.metadata.provider":      "test-provider",
		"langfuse.observation.model.name":             "test-model",
		"langfuse.environment":                        "testing",
		"langfuse.release":                            "v1",
		"langfuse.observation.metadata.finish_reason": "stop",
	} {
		if got := attributes[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if !strings.Contains(attributes["langfuse.observation.input"], "system prompt") ||
		!strings.Contains(attributes["langfuse.observation.input"], "hello") {
		t.Errorf("captured input = %q", attributes["langfuse.observation.input"])
	}
	if got := attributes["langfuse.observation.output"]; !strings.Contains(got, "answer") || strings.Contains(got, "private chain") {
		t.Errorf("captured output = %q", got)
	}
	if attributes["langfuse.observation.completion_start_time"] == "" {
		t.Error("completion start time was not captured")
	}
	if !base.sawSpan {
		t.Error("wrapped model did not receive the generation span context")
	}
}

func TestObservedModelRecordsStartAndStreamErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		model   *testModel
		message string
	}{
		{name: "start", model: &testModel{startErr: errors.New("start failed")}, message: "start failed"},
		{name: "stream", model: &testModel{
			resultErr: errors.New("stream failed"),
			events:    []provider.StreamEvent{{Type: provider.StreamFailed, Error: errors.New("stream failed")}},
		}, message: "stream failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { _ = tracerProvider.Shutdown(context.Background()) })
			client := &Client{tracer: tracerProvider.Tracer("test")}
			stream, err := client.ObserveModel(test.model).Start(context.Background(), agentruntime.ModelRequest{})
			if test.name == "start" {
				if err == nil {
					t.Fatal("Start error = nil")
				}
			} else if err != nil || stream == nil {
				t.Fatalf("Start = stream %v, error %v", stream, err)
			}
			span := waitForSpan(t, exporter)
			attributes := spanAttributes(span)
			if got := attributes["langfuse.observation.level"]; got != "ERROR" {
				t.Errorf("level = %q", got)
			}
			if got := attributes["langfuse.observation.status_message"]; !strings.Contains(got, test.message) {
				t.Errorf("status message = %q", got)
			}
			if span.Status().Code != codes.Error {
				t.Errorf("status code = %v", span.Status().Code)
			}
		})
	}
}

func TestObservedModelPreservesCapabilitiesAndIsIdempotent(t *testing.T) {
	tracerProvider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tracerProvider.Shutdown(context.Background()) })
	client := &Client{tracer: tracerProvider.Tracer("test")}
	base := &testModel{}
	observed := client.ObserveModel(base)
	if _, ok := observed.(agentruntime.ModelMetadataProvider); !ok {
		t.Error("ModelMetadataProvider capability was lost")
	}
	if _, ok := observed.(agentruntime.ContextEstimatorProvider); !ok {
		t.Error("ContextEstimatorProvider capability was lost")
	}
	if identity, ok := observed.(agentruntime.ModelIdentityProvider); !ok || identity.ModelIdentity().Model != "test-model" {
		t.Errorf("ModelIdentityProvider = %#v, %v", identity, ok)
	}
	if again := client.ObserveModel(observed); again != observed {
		t.Error("ObserveModel decorated the same model twice")
	}
}

type testModel struct {
	events    []provider.StreamEvent
	result    provider.StreamResult
	resultErr error
	startErr  error
	sawSpan   bool
}

func (m *testModel) Start(ctx context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.sawSpan = trace.SpanContextFromContext(ctx).IsValid()
	if m.startErr != nil {
		return nil, m.startErr
	}
	return replayStream{events: m.events, result: m.result, err: m.resultErr}, nil
}

func (*testModel) ModelIdentity() agentruntime.ModelIdentity {
	return agentruntime.ModelIdentity{Provider: "test-provider", Model: "test-model"}
}

func (*testModel) ModelMetadata() (agentruntime.ModelMetadata, error) {
	return agentruntime.ModelMetadata{ContextWindowTokens: 100, MaxOutputTokens: 20}, nil
}

func (*testModel) ContextEstimator() agentruntime.ContextEstimator {
	return agentruntime.GenericContextEstimator{}
}

type replayStream struct {
	events []provider.StreamEvent
	result provider.StreamResult
	err    error
}

func (s replayStream) Subscribe(context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, len(s.events))
	for _, event := range s.events {
		events <- event
	}
	close(events)
	return events
}

func (s replayStream) Result() (provider.StreamResult, error) {
	return s.result, s.err
}

func waitForSpan(t *testing.T, exporter *tracetest.InMemoryExporter) sdktrace.ReadOnlySpan {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		spans := exporter.GetSpans()
		if len(spans) != 0 {
			return spans[0].Snapshot()
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for observed span")
		}
		time.Sleep(time.Millisecond)
	}
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[string]string {
	attributes := make(map[string]string)
	for _, item := range span.Attributes() {
		attributes[string(item.Key)] = item.Value.AsString()
	}
	return attributes
}
