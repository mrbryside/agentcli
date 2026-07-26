package agentruntime

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

func TestRuntimeLoggerRecordsOutputGuardRepairWithoutFeedback(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	model := &scriptedRuntimeModel{streams: []ModelStream{
		scriptedStream{events: []provider.StreamEvent{{
			Type: provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
				Content: "unsafe answer", Finished: true,
			}},
		}}},
		scriptedStream{events: []provider.StreamEvent{{
			Type: provider.StreamCompleted,
			Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
				Content: "safe answer", Finished: true,
			}},
		}}},
	}}
	runtime, err := New(context.Background(), Config{
		Model: model, Messages: inmemory.NewMessageStorage(), Logger: logger,
		ToolRequests: make(chan ToolRequest, 1), ToolResults: make(chan ToolResultEnvelope, 1), ToolInterrupts: make(chan ToolInterrupt, 1),
		OutputGuard: func(_ context.Context, attempt OutputGuardAttempt) (OutputGuardDecision, error) {
			if attempt.RetryCount == 0 {
				return OutputGuardDecision{Action: OutputRetry, Feedback: "secret policy feedback"}, nil
			}
			return OutputGuardDecision{Action: OutputProceed}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Start(context.Background(), Request{
		SessionID: "logging-output-guard",
		TurnID:    "turn-1",
		Message:   Message{Type: MessageTypeUser, Content: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRuntimeEvents(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}

	output := logs.String()
	for _, required := range []string{
		`msg="agent repair requested"`,
		`repair_type=output_guard`,
		`attempt=1`,
		`output_guard_retries=1`,
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("logs do not contain %q:\n%s", required, output)
		}
	}
	if strings.Contains(output, "secret policy feedback") {
		t.Fatalf("output-guard feedback leaked into logs:\n%s", output)
	}
}

func TestFormatRuntimeLogValueRedactsSecrets(t *testing.T) {
	value := formatRuntimeLogValue([]byte(`{"token":"secret","nested":{"api_key":"key","safe":"visible"}}`))
	if strings.Contains(value, "secret") || strings.Contains(value, `"key"`) {
		t.Fatalf("formatted log value leaked a secret: %s", value)
	}
	if !strings.Contains(value, `"safe":"visible"`) || strings.Count(value, "[redacted]") != 2 {
		t.Fatalf("formatted log value = %s", value)
	}
}
