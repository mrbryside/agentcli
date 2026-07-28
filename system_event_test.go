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

func TestCloneSystemEventClonesTaskCompletedMetadata(t *testing.T) {
	event := SystemEvent{
		Type:               SystemTaskCompleted,
		MainAgentSessionID: "main-session",
		MainAgentTurnID:    "main-turn",
		TaskCompleted: &TaskCompletedEvent{
			TaskID:            "task-1",
			SubagentSessionID: "subagent-session",
			SubagentTurnID:    "subagent-turn",
			AgentName:         "operator",
			State:             TaskStateCompleted,
			Metadata: map[string]any{
				"requires_requester_reply": true,
				"nested":                   map[string]any{"source": "discord"},
			},
		},
	}

	clone := cloneSystemEvent(event)
	clone.TaskCompleted.Metadata["requires_requester_reply"] = false
	clone.TaskCompleted.Metadata["nested"].(map[string]any)["source"] = "changed"

	if got := event.TaskCompleted.Metadata["requires_requester_reply"]; got != true {
		t.Fatalf("original metadata boolean = %#v", got)
	}
	if got := event.TaskCompleted.Metadata["nested"].(map[string]any)["source"]; got != "discord" {
		t.Fatalf("original nested metadata = %#v", got)
	}
}
