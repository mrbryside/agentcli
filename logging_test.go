package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestAgentLoggerRecordsRequiredToolRepairLifecycle(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	model := &requiredTriggerToolModel{}
	agent, err := New(
		context.Background(),
		WithModel(model),
		WithLogger(logger),
		WithTool(toolexecution.Tool{
			Definition: agentruntime.ToolDefinition{
				Name:        "report",
				Description: "Required final report.",
				InputSchema: agentruntime.ToolSchema{Type: "object"},
			},
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
			Trigger: toolexecution.EndTurn,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("logging-repair"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}

	output := logs.String()
	for _, required := range []string{
		`msg="agent repair requested"`,
		`repair_type=completion_guard`,
		`attempt=1`,
		`tool_allowlist=[report]`,
		`msg="agent turn completed"`,
		`completion_repairs=1`,
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("logs do not contain %q:\n%s", required, output)
		}
	}
}
