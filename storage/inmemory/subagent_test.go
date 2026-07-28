package inmemory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/storage"
)

func TestSubagentStorageCreateGetAndListByMainAgent(t *testing.T) {
	t.Parallel()
	store := NewSubagentStorage()
	first := inMemorySubagent("subagent_a", "mainAgent_a", time.Date(2026, time.July, 20, 0, 0, 0, 0, time.UTC))
	second := inMemorySubagent("subagent_b", "mainAgent_a", first.CreatedAt.Add(time.Second))
	other := inMemorySubagent("subagent_c", "mainAgent_b", first.CreatedAt)
	for _, record := range []storage.Subagent{second, other, first} {
		if _, err := store.Create(context.Background(), record); err != nil {
			t.Fatalf("Create(%s): %v", record.ID, err)
		}
	}

	got, ok, err := store.Get(context.Background(), first.ID)
	if err != nil || !ok || got.ID != first.ID || got.Version != 1 {
		t.Fatalf("Get = (%#v, %t, %v), want stored record, true, nil", got, ok, err)
	}
	list, err := store.ListByMainAgent(context.Background(), "mainAgent_a")
	if err != nil {
		t.Fatalf("ListByMainAgent: %v", err)
	}
	if ids := subagentIDs(list); !equalStrings(ids, []string{"subagent_a", "subagent_b"}) {
		t.Fatalf("list IDs = %v, want stable creation order", ids)
	}
	if list, err := store.ListByMainAgent(context.Background(), "missing"); err != nil || len(list) != 0 {
		t.Fatalf("ListByMainAgent missing = (%v, %v), want empty, nil", list, err)
	}
}

func TestSubagentStorageRejectsDuplicateIDsAndReturnsCopies(t *testing.T) {
	t.Parallel()
	store := NewSubagentStorage()
	record := inMemorySubagent("subagent_a", "mainAgent_a", time.Now())
	if _, err := store.Create(context.Background(), record); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Create(context.Background(), record); !errors.Is(err, storage.ErrDuplicateSubagentID) {
		t.Fatalf("duplicate Create error = %v, want ErrDuplicateSubagentID", err)
	}
	record.Pending[0].Content = "changed before read"
	got, _, err := store.Get(context.Background(), record.ID)
	if err != nil || got.Pending[0].Content != "queued" {
		t.Fatalf("stored input changed: %#v, %v", got, err)
	}
	got.Pending[0].Content = "changed through result"
	gotAgain, _, _ := store.Get(context.Background(), record.ID)
	if gotAgain.Pending[0].Content != "queued" {
		t.Fatalf("stored result changed: %#v", gotAgain)
	}
}

func TestSubagentStorageUpdateUsesVersionCompareAndPreservesOwnership(t *testing.T) {
	t.Parallel()
	store := NewSubagentStorage()
	record := inMemorySubagent("subagent_a", "mainAgent_a", time.Now())
	created, err := store.Create(context.Background(), record)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := store.Update(context.Background(), record.ID, created.Version, storage.SubagentUpdate{
		Status: storage.SubagentStatusRunning, CurrentSubagentTurnID: "turn_2", LastSubagentTurnID: "turn_2", LastResultError: "provider failed", LastResultStatus: storage.SubagentResultFailed,
	})
	if err != nil || updated.Status != storage.SubagentStatusRunning || updated.LastResultError != "provider failed" || updated.Version != created.Version+1 {
		t.Fatalf("Update = (%#v, %v)", updated, err)
	}
	if _, err := store.Update(context.Background(), record.ID, created.Version, storage.SubagentUpdate{Status: ""}); !errors.Is(err, storage.ErrSubagentVersionConflict) {
		t.Fatalf("stale Update error = %v, want ErrSubagentVersionConflict", err)
	}
	if _, err := store.Update(context.Background(), "missing", 1, storage.SubagentUpdate{Status: ""}); !errors.Is(err, storage.ErrSubagentNotFound) {
		t.Fatalf("missing Update error = %v, want ErrSubagentNotFound", err)
	}
}

func TestSubagentStorageUpdateReplacesActiveTaskDelivery(t *testing.T) {
	t.Parallel()
	store := NewSubagentStorage()
	record := inMemorySubagent("subagent_a", "mainAgent_a", time.Now())
	created, err := store.Create(context.Background(), record)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first := storage.TaskDelivery{MainAgentTurnID: "mainAgent_turn_2", AssignmentID: "assignment_1"}
	updated, err := store.Update(context.Background(), record.ID, created.Version, storage.SubagentUpdate{
		Status:             "",
		ActiveTaskDelivery: &first,
	})
	if err != nil {
		t.Fatalf("Update first delivery: %v", err)
	}
	second := storage.TaskDelivery{MainAgentTurnID: "mainAgent_turn_3", AssignmentID: "assignment_2"}
	updated, err = store.Update(context.Background(), record.ID, updated.Version, storage.SubagentUpdate{
		Status:             "",
		ActiveTaskDelivery: &second,
	})
	if err != nil {
		t.Fatalf("Update replacement delivery: %v", err)
	}
	if updated.MainAgentTurnID != "mainAgent_turn" {
		t.Fatalf("original MainAgentTurnID = %q, want mainAgent_turn", updated.MainAgentTurnID)
	}
	if updated.ActiveTaskDelivery == nil || updated.ActiveTaskDelivery.MainAgentTurnID != "mainAgent_turn_3" || updated.ActiveTaskDelivery.AssignmentID != "assignment_2" {
		t.Fatalf("active delivery = %#v, want replacement", updated.ActiveTaskDelivery)
	}
	second.AssignmentID = "mutated"
	got, ok, err := store.Get(context.Background(), record.ID)
	if err != nil || !ok {
		t.Fatalf("Get = (%#v, %t, %v)", got, ok, err)
	}
	if got.ActiveTaskDelivery == nil || got.ActiveTaskDelivery.AssignmentID != "assignment_2" {
		t.Fatalf("stored active delivery = %#v, want defensive replacement copy", got.ActiveTaskDelivery)
	}
}

func TestSubagentStorageMailboxIsFIFOAndCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	store := NewSubagentStorage()
	record := inMemorySubagent("subagent_a", "mainAgent_a", time.Now())
	created, err := store.Create(context.Background(), record)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first := storage.SubagentQueuedMessage{ID: "next_1", Content: "first", CreatedAt: time.Now()}
	second := storage.SubagentQueuedMessage{ID: "next_2", Content: "second", CreatedAt: time.Now()}
	if _, err := store.Enqueue(context.Background(), record.ID, first); err != nil {
		t.Fatalf("Enqueue first: %v", err)
	}
	if _, err := store.Enqueue(context.Background(), record.ID, second); err != nil {
		t.Fatalf("Enqueue second: %v", err)
	}
	for _, want := range []string{"queued", "first", "second"} {
		_, message, err := store.Dequeue(context.Background(), record.ID)
		if err != nil || message == nil || message.Content != want {
			t.Fatalf("Dequeue = (%#v, %v), want %q", message, err, want)
		}
	}
	if _, message, err := store.Dequeue(context.Background(), record.ID); err != nil || message != nil {
		t.Fatalf("empty Dequeue = (%#v, %v), want nil, nil", message, err)
	}
	closed, err := store.Close(context.Background(), record.ID)
	if err != nil || closed.Status != storage.SubagentStatusClosed || closed.ClosedAt == nil || len(closed.Pending) != 0 {
		t.Fatalf("Close = (%#v, %v)", closed, err)
	}
	again, err := store.Close(context.Background(), record.ID)
	if err != nil || again.Version != closed.Version || !again.ClosedAt.Equal(*closed.ClosedAt) {
		t.Fatalf("second Close = (%#v, %v), want idempotent close", again, err)
	}
	if _, err := store.Enqueue(context.Background(), record.ID, first); !errors.Is(err, storage.ErrSubagentClosed) {
		t.Fatalf("Enqueue closed error = %v, want ErrSubagentClosed", err)
	}
	_ = created
}

func TestSubagentStorageObserveAndContextCancellation(t *testing.T) {
	t.Parallel()
	store := NewSubagentStorage()
	record := inMemorySubagent("subagent_a", "mainAgent_a", time.Now())
	if _, err := store.Create(context.Background(), record); err != nil {
		t.Fatalf("Create: %v", err)
	}
	observed, err := store.Observe(context.Background(), record.ID, "message_3", 3)
	if err != nil || observed.ObservedMessageID != "message_3" || observed.ObservedVersion != 3 {
		t.Fatalf("Observe = (%#v, %v)", observed, err)
	}
	stale, err := store.Observe(context.Background(), record.ID, "message_2", 2)
	if err != nil || stale.ObservedMessageID != "message_3" || stale.ObservedVersion != 3 {
		t.Fatalf("stale Observe = (%#v, %v)", stale, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.Get(cancelled, record.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Get error = %v, want context.Canceled", err)
	}
	if _, err := store.Observe(cancelled, record.ID, "message_4", 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Observe error = %v, want context.Canceled", err)
	}
}

func TestSubagentStorageConcurrentIndependentMainAgents(t *testing.T) {
	store := NewSubagentStorage()
	const mainAgents, perMainAgent = 4, 50
	var group sync.WaitGroup
	for mainAgent := 0; mainAgent < mainAgents; mainAgent++ {
		group.Add(1)
		go func(mainAgent int) {
			defer group.Done()
			mainAgentID := fmt.Sprintf("mainAgent_%d", mainAgent)
			for index := 0; index < perMainAgent; index++ {
				id := fmt.Sprintf("subagent_%d_%03d", mainAgent, index)
				if _, err := store.Create(context.Background(), inMemorySubagent(id, mainAgentID, time.Now())); err != nil {
					t.Errorf("Create(%s): %v", id, err)
				}
			}
		}(mainAgent)
	}
	group.Wait()
	for mainAgent := 0; mainAgent < mainAgents; mainAgent++ {
		list, err := store.ListByMainAgent(context.Background(), fmt.Sprintf("mainAgent_%d", mainAgent))
		if err != nil || len(list) != perMainAgent {
			t.Fatalf("mainAgent %d list = %d, %v; want %d", mainAgent, len(list), err, perMainAgent)
		}
	}
}

func inMemorySubagent(id, mainAgentID string, created time.Time) storage.Subagent {
	return storage.Subagent{
		ID: id, DisplayName: "Mira", MainAgentSessionID: mainAgentID, MainAgentTurnID: "mainAgent_turn", SubagentSessionID: id + "_session",
		DefinitionName: "researcher", Provider: "openai", Model: "gpt-test", Status: "",
		Pending:   []storage.SubagentQueuedMessage{{ID: "queued", Content: "queued", CreatedAt: created}},
		CreatedAt: created, UpdatedAt: created,
	}
}

func subagentIDs(records []storage.Subagent) []string {
	ids := make([]string, len(records))
	for index, record := range records {
		ids[index] = record.ID
	}
	return ids
}
