package agentcli

import (
	"context"
	"fmt"
	"time"

	langfuseobs "github.com/mrbryside/agentcli/observability/langfuse"
)

func newProjectLangfuse(ctx context.Context, project *Project) (*langfuseobs.Client, bool, error) {
	if project == nil || project.config.Observability == nil || project.config.Observability.Langfuse == nil {
		return nil, false, nil
	}
	config := project.config.Observability.Langfuse
	if !config.Enabled {
		return nil, false, nil
	}
	sampleRate := 1.0
	if config.SampleRate != nil {
		sampleRate = *config.SampleRate
	}
	client, err := langfuseobs.New(ctx, langfuseobs.Config{
		BaseURL:     config.BaseURL,
		PublicKey:   config.PublicKey,
		SecretKey:   config.SecretKey,
		Environment: config.Environment,
		ServiceName: config.ServiceName,
		Release:     config.Release,
		SampleRate:  sampleRate,
		Capture: langfuseobs.CaptureConfig{
			Input:     config.Capture.Input,
			Output:    config.Capture.Output,
			Reasoning: config.Capture.Reasoning,
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("create Langfuse observability: %w", err)
	}
	return client, true, nil
}

func shutdownOwnedLangfuse(client *langfuseobs.Client, owned bool) {
	if client == nil || !owned {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = client.Shutdown(ctx)
	cancel()
}
