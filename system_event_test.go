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
		Type:               SystemSubagentClosed,
		MainAgentSessionID: "main-agent",
		MainAgentTurnID:    "turn",
		SubagentClosed: &SubagentClosedEvent{
			Subagent: storage.Subagent{
				ID:                "subagent",
				SubagentSessionID: "subagent-session",
				DisplayName:       "Research",
				DefinitionName:    "researcher",
				Status:            storage.SubagentStatusClosed,
				LastResultStatus:  storage.SubagentResultCompleted,
			},
			PreviousStatus:       storage.SubagentStatusIdle,
			PreviousResultStatus: storage.SubagentResultCompleted,
			Automatic:            true,
		},
	})

	output := logs.String()
	for _, required := range []string{
		`msg="subagent closed"`,
		`msg="subagent closed details"`,
		`main_agent_session_id=main-agent`,
		`main_agent_turn_id=turn`,
		`subagent_id=subagent`,
		`automatic=true`,
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("system logs do not contain %q:\n%s", required, output)
		}
	}
}
