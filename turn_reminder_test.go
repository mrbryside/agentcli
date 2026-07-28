package agentcli

import (
	"context"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
)

func TestNewTurnContextReminderAppearsOnlyOnFirstProviderStep(t *testing.T) {
	provider := newTurnContextReminderProvider()

	first, err := provider(context.Background(), agentruntime.ContextReminderRequest{
		SessionID: "session", TurnID: `turn-"one"`, ProviderStep: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("first-step reminders = %#v, want one", first)
	}
	for _, expected := range []string{
		"<turn_start>",
		"state: new",
		`turn_id: "turn-&#34;one&#34;"`,
		"first model request of a new turn",
		"Later model requests without <turn_start> belong to this same turn",
		"Tool results do not start another turn",
	} {
		if !strings.Contains(first[0].Content, expected) {
			t.Fatalf("new-turn reminder does not contain %q: %q", expected, first[0].Content)
		}
	}

	later, err := provider(context.Background(), agentruntime.ContextReminderRequest{
		SessionID: "session", TurnID: `turn-"one"`, ProviderStep: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 0 {
		t.Fatalf("later-step reminders = %#v, want none", later)
	}
}
