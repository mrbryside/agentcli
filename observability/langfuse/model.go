package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type observedModel struct {
	client   *Client
	model    agentruntime.Model
	identity agentruntime.ModelIdentity
}

type observedModelWithMetadata struct {
	*observedModel
	metadata agentruntime.ModelMetadataProvider
}

type observedModelWithEstimator struct {
	*observedModel
	estimator agentruntime.ContextEstimatorProvider
}

type observedModelWithMetadataAndEstimator struct {
	*observedModel
	metadata  agentruntime.ModelMetadataProvider
	estimator agentruntime.ContextEstimatorProvider
}

func newObservedModel(client *Client, model agentruntime.Model) agentruntime.Model {
	base := &observedModel{client: client, model: model}
	if identity, ok := model.(agentruntime.ModelIdentityProvider); ok {
		base.identity = identity.ModelIdentity()
	}
	metadata, hasMetadata := model.(agentruntime.ModelMetadataProvider)
	estimator, hasEstimator := model.(agentruntime.ContextEstimatorProvider)
	switch {
	case hasMetadata && hasEstimator:
		return &observedModelWithMetadataAndEstimator{observedModel: base, metadata: metadata, estimator: estimator}
	case hasMetadata:
		return &observedModelWithMetadata{observedModel: base, metadata: metadata}
	case hasEstimator:
		return &observedModelWithEstimator{observedModel: base, estimator: estimator}
	default:
		return base
	}
}

func (m *observedModel) Start(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attributes := []attribute.KeyValue{
		attribute.String("langfuse.observation.type", "generation"),
		attribute.String("langfuse.trace.name", "llm-call"),
	}
	if m.identity.Provider != "" {
		attributes = append(attributes,
			attribute.String("langfuse.observation.metadata.provider", m.identity.Provider),
		)
	}
	if m.identity.Model != "" {
		attributes = append(attributes,
			attribute.String("langfuse.observation.model.name", m.identity.Model),
		)
	}
	if request.SessionID != "" {
		attributes = append(attributes, attribute.String("langfuse.session.id", request.SessionID))
	}
	if request.TurnID != "" {
		attributes = append(attributes,
			attribute.String("langfuse.observation.metadata.turn_id", request.TurnID),
		)
	}
	if m.client.config.Environment != "" {
		attributes = append(attributes, attribute.String("langfuse.environment", m.client.config.Environment))
	}
	if m.client.config.Release != "" {
		attributes = append(attributes, attribute.String("langfuse.release", m.client.config.Release))
	}
	if request.MaxOutputTokens > 0 {
		if parameters, err := json.Marshal(map[string]int{"max_output_tokens": request.MaxOutputTokens}); err == nil {
			attributes = append(attributes, attribute.String("langfuse.observation.model.parameters", string(parameters)))
		}
	}
	if m.client.config.Capture.Input {
		if input, err := json.Marshal(request.Clone()); err == nil {
			attributes = append(attributes, attribute.String("langfuse.observation.input", string(input)))
		}
	}

	callCtx, span := m.client.tracer.Start(ctx, "llm-call",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attributes...),
	)
	stream, err := m.model.Start(callCtx, request)
	if err != nil {
		finishSpanError(span, err)
		return nil, err
	}
	if stream == nil {
		finishSpanError(span, errors.New("model returned a nil stream"))
		return nil, nil
	}
	observed := &observedStream{
		ModelStream: stream,
		span:        span,
		capture:     m.client.config.Capture,
	}
	go observed.monitor()
	return observed, nil
}

func (m *observedModel) observedBy(client *Client) bool {
	return m != nil && m.client == client
}

func (m *observedModel) ModelIdentity() agentruntime.ModelIdentity {
	if m == nil {
		return agentruntime.ModelIdentity{}
	}
	return m.identity
}

func (m *observedModelWithMetadata) ModelMetadata() (agentruntime.ModelMetadata, error) {
	return m.metadata.ModelMetadata()
}

func (m *observedModelWithEstimator) ContextEstimator() agentruntime.ContextEstimator {
	return m.estimator.ContextEstimator()
}

func (m *observedModelWithMetadataAndEstimator) ModelMetadata() (agentruntime.ModelMetadata, error) {
	return m.metadata.ModelMetadata()
}

func (m *observedModelWithMetadataAndEstimator) ContextEstimator() agentruntime.ContextEstimator {
	return m.estimator.ContextEstimator()
}

type observedStream struct {
	agentruntime.ModelStream
	span    trace.Span
	capture CaptureConfig

	finishOnce          sync.Once
	completionStartOnce sync.Once
}

func (s *observedStream) monitor() {
	for event := range s.ModelStream.Subscribe(context.Background()) {
		switch event.Type {
		case provider.ContentReceived, provider.ReasoningReceived, provider.ToolCallStarted, provider.ToolArgumentsReceived:
			s.markCompletionStarted()
		case provider.StreamFailed:
			err := event.Error
			if payload, ok := event.Payload.(provider.StreamFailedPayload); ok && payload.Error != nil {
				err = payload.Error
			}
			if err == nil {
				err = errors.New("model stream failed")
			}
			s.finishError(err)
			return
		case provider.StreamCompleted:
			result, err := s.ModelStream.Result()
			if err != nil {
				s.finishError(err)
				return
			}
			s.finishSuccess(result, event.FinishReason)
			return
		}
	}
	result, err := s.ModelStream.Result()
	if err != nil {
		s.finishError(err)
		return
	}
	s.finishSuccess(result, "")
}

func (s *observedStream) markCompletionStarted() {
	s.completionStartOnce.Do(func() {
		s.span.SetAttributes(attribute.String(
			"langfuse.observation.completion_start_time",
			time.Now().UTC().Format(time.RFC3339Nano),
		))
	})
}

func (s *observedStream) finishSuccess(result provider.StreamResult, finishReason string) {
	s.finishOnce.Do(func() {
		if finishReason != "" {
			s.span.SetAttributes(attribute.String("langfuse.observation.metadata.finish_reason", finishReason))
		}
		output := make(map[string]any)
		if s.capture.Output {
			output["content"] = result.Content
			if len(result.CompletedTools) != 0 {
				output["tool_calls"] = result.CompletedTools
			}
		}
		if s.capture.Reasoning {
			output["reasoning"] = result.Reasoning
		}
		if len(output) != 0 {
			if encoded, err := json.Marshal(output); err == nil {
				s.span.SetAttributes(attribute.String("langfuse.observation.output", string(encoded)))
			}
		}
		s.span.SetStatus(codes.Ok, "")
		s.span.End()
	})
}

func (s *observedStream) finishError(err error) {
	s.finishOnce.Do(func() {
		finishSpanError(s.span, err)
	})
}

func finishSpanError(span trace.Span, err error) {
	if err == nil {
		err = errors.New("model call failed")
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(
		attribute.String("langfuse.observation.level", "ERROR"),
		attribute.String("langfuse.observation.status_message", err.Error()),
	)
	span.End()
}
