package inmemory

import (
	"errors"
	"testing"
	"time"

	"harness-api/confirmation"
)

func TestConfirmationStorageTracksLateYesNoAndCancellation(t *testing.T) {
	store := NewConfirmationStorage()
	now := time.Now().UTC()
	request := confirmation.Request{ID: "confirm_1", SessionID: "session", TurnID: "turn", CallID: "call", ToolName: "publish", Message: "Publish?", CreatedAt: now}
	if err := store.Create(request); err != nil {
		t.Fatal(err)
	}
	if pending := store.Pending("session"); len(pending) != 1 || pending[0].State != confirmation.Pending {
		t.Fatalf("pending = %#v", pending)
	}
	decision := confirmation.Decision{ConfirmationID: request.ID, SessionID: request.SessionID, TurnID: request.TurnID, CallID: request.CallID, Answer: confirmation.Yes}
	record, err := store.Resolve(decision)
	if err != nil || record.State != confirmation.Confirmed {
		t.Fatalf("resolved = %#v, %v", record, err)
	}
	if _, err := store.Resolve(decision); err != nil {
		t.Fatalf("identical late retry is not idempotent: %v", err)
	}
	decision.Answer = confirmation.No
	if _, err := store.Resolve(decision); !errors.Is(err, confirmation.ErrAlreadyResolved) {
		t.Fatalf("conflicting retry error = %v", err)
	}

	second := request
	second.ID, second.CallID = "confirm_2", "call-2"
	if err := store.Create(second); err != nil {
		t.Fatal(err)
	}
	if cancelled := store.Cancel("session", "turn"); len(cancelled) != 1 || cancelled[0].State != confirmation.Cancelled {
		t.Fatalf("cancelled = %#v", cancelled)
	}
}
