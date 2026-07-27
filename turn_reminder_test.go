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
		"<runtime_turn_boundary>",
		"state: new_turn",
		`turn_id: "turn-&#34;one&#34;"`,
		"provider_step: 1",
		"only on the first provider request of a new runtime turn",
		"Later provider requests without this marker continue this same runtime turn",
		"does not create another load trigger by itself",
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
