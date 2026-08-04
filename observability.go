package agentcli

import (
	"context"
	"fmt"
	"time"

	langfuseobs "github.com/mrbryside/agentcli/observability/langfuse"
)

// LangfuseConfig configures code-owned Langfuse observability.
type LangfuseConfig = langfuseobs.Config

// LangfuseCaptureConfig controls potentially sensitive generation payloads.
type LangfuseCaptureConfig = langfuseobs.CaptureConfig

func newLangfuse(ctx context.Context, config *LangfuseConfig) (*langfuseobs.Client, error) {
	if config == nil {
		return nil, nil
	}
	client, err := langfuseobs.New(ctx, *config)
	if err != nil {
		return nil, fmt.Errorf("create Langfuse observability: %w", err)
	}
	return client, nil
}

func shutdownOwnedLangfuse(client *langfuseobs.Client, owned bool) {
	if client == nil || !owned {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = client.Shutdown(ctx)
	cancel()
}
