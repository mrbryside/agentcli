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
	scopeEvents := agent.SubscribeScopeEvents(context.Background())

	run, err := agent.Start(context.Background(), userRequest("logging-repair"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := run.Result(); err != nil {
		t.Fatal(err)
	}
	for event := range scopeEvents {
		if event.Type == EndScope {
			break
		}
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

func TestRuntimeLogStoreRoutesRecordsToAttachedTerminal(t *testing.T) {
	var fallback bytes.Buffer
	logs := newRuntimeLogStore(&fallback)

	if _, err := logs.Write([]byte("before terminal\n")); err != nil {
		t.Fatal(err)
	}
	updates, detach := logs.attachTerminal()
	if _, err := logs.Write([]byte("inside terminal\n")); err != nil {
		t.Fatal(err)
	}
	if got := fallback.String(); got != "before terminal\n" {
		t.Fatalf("fallback output while attached = %q", got)
	}
	select {
	case entry := <-updates:
		if entry.sequence != 2 || entry.text != "inside terminal\n" {
			t.Fatalf("terminal update = %#v", entry)
		}
	default:
		t.Fatal("attached terminal did not receive a log update")
	}

	detach()
	detach()
	if _, err := logs.Write([]byte("after terminal\n")); err != nil {
		t.Fatal(err)
	}
	if got := fallback.String(); got != "before terminal\nafter terminal\n" {
		t.Fatalf("fallback output after detach = %q", got)
	}
	entries := logs.snapshot()
	if len(entries) != 3 || entries[0].text != "before terminal\n" || entries[2].text != "after terminal\n" {
		t.Fatalf("log snapshot = %#v", entries)
	}
}

func TestRuntimeLogStoreBoundsRetainedRecords(t *testing.T) {
	logs := newRuntimeLogStore(nil)
	for index := 0; index < runtimeLogEntryLimit+3; index++ {
		if _, err := logs.Write([]byte("record\n")); err != nil {
			t.Fatal(err)
		}
	}
	entries := logs.snapshot()
	if len(entries) != runtimeLogEntryLimit {
		t.Fatalf("retained entries = %d, want %d", len(entries), runtimeLogEntryLimit)
	}
	if entries[0].sequence != 4 || entries[len(entries)-1].sequence != runtimeLogEntryLimit+3 {
		t.Fatalf("retained sequence range = %d..%d", entries[0].sequence, entries[len(entries)-1].sequence)
	}
}

func TestRuntimeLogStoreOverflowKeepsNewestUpdate(t *testing.T) {
	logs := newRuntimeLogStore(nil)
	updates, detach := logs.attachTerminal()
	defer detach()
	const count = 300
	for index := 0; index < count; index++ {
		if _, err := logs.Write([]byte("record\n")); err != nil {
			t.Fatal(err)
		}
	}
	var last runtimeLogEntry
	for {
		select {
		case last = <-updates:
			continue
		default:
			if last.sequence != count {
				t.Fatalf("last retained update sequence = %d, want %d", last.sequence, count)
			}
			return
		}
	}
}

func TestWithLoggerKeepsCallerOwnedRouting(t *testing.T) {
	configuration := defaultConfig(t.TempDir())
	configuration.runtimeLogs = newRuntimeLogStore(nil)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if err := WithLogger(logger)(&configuration); err != nil {
		t.Fatal(err)
	}
	if configuration.logger != logger || configuration.runtimeLogs != nil {
		t.Fatalf("caller logger routing = (%p, %p)", configuration.logger, configuration.runtimeLogs)
	}
}

func TestWithLogLevelConfiguresTerminalLogCapture(t *testing.T) {
	configuration := defaultConfig(t.TempDir())
	if err := WithLogLevel(slog.LevelDebug)(&configuration); err != nil {
		t.Fatal(err)
	}
	if configuration.logger == nil || configuration.runtimeLogs == nil {
		t.Fatalf("managed logger = (%p, %p)", configuration.logger, configuration.runtimeLogs)
	}
	if !configuration.logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("managed logger did not enable debug records")
	}
}
