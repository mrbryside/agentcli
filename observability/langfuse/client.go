// Package langfuse instruments provider-neutral model calls and exports them
// to Langfuse through OpenTelemetry's OTLP/HTTP protocol.
package langfuse

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/mrbryside/agentcli/agentruntime"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultBaseURL      = "https://cloud.langfuse.com"
	instrumentationName = "github.com/mrbryside/agentcli/observability/langfuse"
)

var environmentPattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// CaptureConfig controls generation payloads that may contain sensitive data.
type CaptureConfig struct {
	Input     bool
	Output    bool
	Reasoning bool
}

// Config configures a Langfuse OTLP exporter. SampleRate must be in [0, 1].
type Config struct {
	BaseURL     string
	PublicKey   string
	SecretKey   string
	Environment string
	ServiceName string
	Release     string
	SampleRate  float64
	Capture     CaptureConfig
}

// Client owns one tracer provider and can decorate any provider-neutral Model.
// It is safe to share one Client across main, compaction, guard, and subagent
// models.
type Client struct {
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
	config   Config
}

// New creates a batched OTLP/HTTP exporter. It performs no network request;
// export failures are handled asynchronously by OpenTelemetry and never alter
// model-call results.
func New(ctx context.Context, config Config) (*Client, error) {
	config = normalizeConfig(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := strings.TrimRight(config.BaseURL, "/") + "/api/public/otel/v1/traces"
	auth := base64.StdEncoding.EncodeToString([]byte(config.PublicKey + ":" + config.SecretKey))
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization":                "Basic " + auth,
			"x-langfuse-ingestion-version": "4",
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("create Langfuse OTLP exporter: %w", err)
	}

	resourceAttributes := []attribute.KeyValue{
		attribute.String("service.name", config.ServiceName),
	}
	if config.Environment != "" {
		resourceAttributes = append(resourceAttributes, attribute.String("deployment.environment.name", config.Environment))
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(config.SampleRate)),
		sdktrace.WithResource(resource.NewSchemaless(resourceAttributes...)),
	)
	return &Client{
		provider: provider,
		tracer:   provider.Tracer(instrumentationName),
		config:   config,
	}, nil
}

// ObserveModel returns an idempotent model decorator that emits one generation
// span per Start call and keeps the span open until the stream terminates.
func (c *Client) ObserveModel(model agentruntime.Model) agentruntime.Model {
	if c == nil || isNilModel(model) {
		return model
	}
	if observed, ok := model.(interface{ observedBy(*Client) bool }); ok && observed.observedBy(c) {
		return model
	}
	return newObservedModel(c, model)
}

func isNilModel(model agentruntime.Model) bool {
	if model == nil {
		return true
	}
	value := reflect.ValueOf(model)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Shutdown flushes queued spans and releases exporter resources.
func (c *Client) Shutdown(ctx context.Context) error {
	if c == nil || c.provider == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.provider.Shutdown(ctx)
}

func normalizeConfig(config Config) Config {
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	config.PublicKey = strings.TrimSpace(config.PublicKey)
	config.SecretKey = strings.TrimSpace(config.SecretKey)
	config.Environment = strings.TrimSpace(config.Environment)
	config.ServiceName = strings.TrimSpace(config.ServiceName)
	if config.ServiceName == "" {
		config.ServiceName = "agentcli"
	}
	config.Release = strings.TrimSpace(config.Release)
	return config
}

func validateConfig(config Config) error {
	if config.PublicKey == "" {
		return errors.New("Langfuse public key is required")
	}
	if config.SecretKey == "" {
		return errors.New("Langfuse secret key is required")
	}
	if config.SampleRate < 0 || config.SampleRate > 1 {
		return errors.New("Langfuse sample rate must be between 0 and 1")
	}
	parsedBaseURL, err := url.Parse(config.BaseURL)
	if err != nil || parsedBaseURL.Host == "" || (parsedBaseURL.Scheme != "http" && parsedBaseURL.Scheme != "https") || parsedBaseURL.RawQuery != "" || parsedBaseURL.Fragment != "" {
		return errors.New("Langfuse base URL must be an absolute HTTP(S) URL without query or fragment")
	}
	if environment := config.Environment; environment != "" {
		if len(environment) > 40 || strings.HasPrefix(environment, "langfuse") || !environmentPattern.MatchString(environment) {
			return errors.New("Langfuse environment must be at most 40 lowercase letters, numbers, hyphens, or underscores and must not start with langfuse")
		}
	}
	return nil
}
