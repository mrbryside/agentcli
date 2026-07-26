package agentcli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/storage"
)

func TestSubagentSystemEventLoggingIsFrameworkOwned(t *testing.T) {
	var logs bytes.Buffer
	manager := &subagentManager{config: config{
		logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}}
	manager.publishSystemEvent(SystemEvent{
		Type:      SystemSubagentClosed,
		SessionID: "parent",
		TurnID:    "turn",
		SubagentClosed: &SubagentClosedEvent{
			Subagent: storage.Subagent{
				ID:              "child",
				SessionID:       "child-session",
				DisplayName:     "Research",
				DefinitionName:  "researcher",
				Status:          storage.SubagentStatusClosed,
				LastTurnOutcome: storage.SubagentTurnCompleted,
			},
			PreviousStatus:  storage.SubagentStatusIdle,
			PreviousOutcome: storage.SubagentTurnCompleted,
			Automatic:       true,
		},
	})

	output := logs.String()
	for _, required := range []string{
		`msg="subagent closed"`,
		`msg="subagent closed details"`,
		`session_id=parent`,
		`turn_id=turn`,
		`subagent_id=child`,
		`automatic=true`,
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("system logs do not contain %q:\n%s", required, output)
		}
	}
}
