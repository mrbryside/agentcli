package inmemory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/mrbryside/agentcli/storage"
)

func TestMessageStorageAppendAndListPreserveOrder(t *testing.T) {
	t.Parallel()

	store := NewMessageStorage()
	first := testMessage("message-1", "session-1", "turn-1", "first")
	second := testMessage("message-2", "session-1", "turn-1", "second")
	third := testMessage("message-3", "session-1", "turn-2", "third")

	if err := store.Append(context.Background(), first, second); err != nil {
		t.Fatalf("Append first batch: %v", err)
	}
	if err := store.Append(context.Background(), third); err != nil {
		t.Fatalf("Append second batch: %v", err)
	}

	messages, err := store.List(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := messageIDs(messages), []string{"message-1", "message-2", "message-3"}; !equalStrings(got, want) {
		t.Fatalf("message IDs = %v, want %v", got, want)
	}
}

func TestMessageStorageAppendIsAtomic(t *testing.T) {
	t.Parallel()

	store := NewMessageStorage()
	original := testMessage("message-1", "session-1", "turn-1", "original")
	if err := store.Append(context.Background(), original); err != nil {
		t.Fatalf("Append original: %v", err)
	}

	err := store.Append(
		context.Background(),
		testMessage("message-2", "session-1", "turn-1", "not retained"),
		testMessage("message-1", "session-1", "turn-1", "duplicate"),
	)
	if !errors.Is(err, storage.ErrDuplicateMessageID) {
		t.Fatalf("Append duplicate error = %v, want ErrDuplicateMessageID", err)
	}

	messages, err := store.List(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := messageIDs(messages), []string{"message-1"}; !equalStrings(got, want) {
		t.Fatalf("message IDs after failed append = %v, want %v", got, want)
	}
}

func TestMessageStorageAppendEmptyIsNoop(t *testing.T) {
	t.Parallel()

	store := NewMessageStorage()
	if err := store.Append(context.Background()); err != nil {
		t.Fatalf("Append empty: %v", err)
	}

	messages, err := store.List(context.Background(), "missing")
	if err != nil {
		t.Fatalf("List missing session: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages = %v, want empty", messages)
	}
}

func TestMessageStorageRejectsDuplicateMessageIDs(t *testing.T) {
	t.Parallel()

	store := NewMessageStorage()
	err := store.Append(
		context.Background(),
		testMessage("message-1", "session-1", "turn-1", "one"),
		testMessage("message-1", "session-1", "turn-1", "two"),
	)
	if !errors.Is(err, storage.ErrDuplicateMessageID) {
		t.Fatalf("Append duplicate batch error = %v, want ErrDuplicateMessageID", err)
	}

	messages, err := store.List(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages after duplicate batch = %v, want empty", messages)
	}
}

func TestMessageStorageTurnExistsAndUnknownSessions(t *testing.T) {
	t.Parallel()

	store := NewMessageStorage()
	if err := store.Append(context.Background(), testMessage("message-1", "session-1", "turn-1", "one")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	for _, test := range []struct {
		name      string
		sessionID string
		turnID    string
		want      bool
	}{
		{name: "existing turn", sessionID: "session-1", turnID: "turn-1", want: true},
		{name: "other turn", sessionID: "session-1", turnID: "turn-2", want: false},
		{name: "unknown session", sessionID: "missing", turnID: "turn-1", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := store.TurnExists(context.Background(), test.sessionID, test.turnID)
			if err != nil {
				t.Fatalf("TurnExists: %v", err)
			}
			if got != test.want {
				t.Fatalf("TurnExists = %t, want %t", got, test.want)
			}
		})
	}
}

func TestMessageStorageListReturnsDefensiveCopies(t *testing.T) {
	t.Parallel()

	store := NewMessageStorage()
	message := testMessage("message-1", "session-1", "turn-1", "original")
	message.ToolCalls = []storage.ToolCall{{CallID: "call-1", Name: "tool", Arguments: []byte(`{"value":1}`)}}
	message.Type = storage.MessageTypeToolCall
	if err := store.Append(context.Background(), message); err != nil {
		t.Fatalf("Append: %v", err)
	}

	first, err := store.List(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("first List: %v", err)
	}
	first[0].Content = "changed"
	first[0].ToolCalls[0].Arguments[0] = '['
	first[0].ToolCalls[0].Name = "changed-tool"

	second, err := store.List(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if second[0].Content != "original" {
		t.Fatalf("stored content = %q, want original", second[0].Content)
	}
	if second[0].ToolCalls[0].Name != "tool" {
		t.Fatalf("stored tool name = %q, want tool", second[0].ToolCalls[0].Name)
	}
	if got, want := string(second[0].ToolCalls[0].Arguments), `{"value":1}`; got != want {
		t.Fatalf("stored arguments = %s, want %s", got, want)
	}
}

func TestMessageStorageConcurrentSessionsAreIsolated(t *testing.T) {
	store := NewMessageStorage()
	const count = 100

	start := make(chan struct{})
	done := make(chan struct{})
	var writers sync.WaitGroup
	for _, sessionID := range []string{"session-a", "session-b"} {
		writers.Add(1)
		go func(sessionID string) {
			defer writers.Done()
			<-start
			for index := 0; index < count; index++ {
				message := testMessage(
					fmt.Sprintf("%s-message-%03d", sessionID, index),
					sessionID,
					"turn-1",
					fmt.Sprintf("%d", index),
				)
				if err := store.Append(context.Background(), message); err != nil {
					t.Errorf("Append %s: %v", sessionID, err)
					return
				}
			}
		}(sessionID)
	}

	var readers sync.WaitGroup
	for _, sessionID := range []string{"session-a", "session-b"} {
		readers.Add(1)
		go func(sessionID string) {
			defer readers.Done()
			<-start
			for {
				messages, err := store.List(context.Background(), sessionID)
				if err != nil {
					t.Errorf("List %s: %v", sessionID, err)
					return
				}
				assertSessionOrder(t, sessionID, messages)
				select {
				case <-done:
					return
				default:
				}
			}
		}(sessionID)
	}

	close(start)
	writers.Wait()
	close(done)
	readers.Wait()

	for _, sessionID := range []string{"session-a", "session-b"} {
		messages, err := store.List(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("final List %s: %v", sessionID, err)
		}
		if len(messages) != count {
			t.Fatalf("message count for %s = %d, want %d", sessionID, len(messages), count)
		}
		assertSessionOrder(t, sessionID, messages)
	}
}

func testMessage(id, sessionID, turnID, content string) storage.Message {
	return storage.Message{
		ID:        id,
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      storage.MessageTypeUser,
		Content:   content,
	}
}

func messageIDs(messages []storage.Message) []string {
	ids := make([]string, len(messages))
	for index, message := range messages {
		ids[index] = message.ID
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func assertSessionOrder(t *testing.T, sessionID string, messages []storage.Message) {
	t.Helper()
	for index, message := range messages {
		if message.SessionID != sessionID {
			t.Errorf("message %d session = %q, want %q", index, message.SessionID, sessionID)
			return
		}
		wantID := fmt.Sprintf("%s-message-%03d", sessionID, index)
		if message.ID != wantID {
			t.Errorf("message %d ID = %q, want %q", index, message.ID, wantID)
			return
		}
	}
}
