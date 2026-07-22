package inmemory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mrbryside/agentcli/storage"
)

// SubagentStorage stores provider-neutral child-session state in process
// memory. A mutex makes each record mutation atomic and every boundary clones
// mutable state.
type SubagentStorage struct {
	mu      sync.RWMutex
	records map[string]storage.Subagent
}

// NewSubagentStorage returns an empty concurrency-safe child-session store.
func NewSubagentStorage() *SubagentStorage {
	return &SubagentStorage{records: make(map[string]storage.Subagent)}
}

// Create stores a new child record and assigns its initial version.
func (s *SubagentStorage) Create(ctx context.Context, subagent storage.Subagent) (storage.Subagent, error) {
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	if err := storage.ValidateSubagent(subagent); err != nil {
		return storage.Subagent{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	if _, exists := s.records[subagent.ID]; exists {
		return storage.Subagent{}, fmt.Errorf("%w: %q", storage.ErrDuplicateSubagentID, subagent.ID)
	}
	subagent = storage.CloneSubagent(subagent)
	subagent.Version = 1
	s.records[subagent.ID] = subagent
	return storage.CloneSubagent(subagent), nil
}

// Get returns a copied child record. Missing records are reported as false.
func (s *SubagentStorage) Get(ctx context.Context, id string) (storage.Subagent, bool, error) {
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, false, err
	}
	s.mu.RLock()
	record, exists := s.records[id]
	s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, false, err
	}
	if !exists {
		return storage.Subagent{}, false, nil
	}
	return storage.CloneSubagent(record), true, nil
}

// ListByParent returns copies in stable creation order, then ID order for ties.
func (s *SubagentStorage) ListByParent(ctx context.Context, parentSessionID string) ([]storage.Subagent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	records := make([]storage.Subagent, 0)
	for _, record := range s.records {
		if record.ParentSessionID == parentSessionID {
			records = append(records, storage.CloneSubagent(record))
		}
	}
	s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, nil
}

// Update compare-safely changes a child's lifecycle state.
func (s *SubagentStorage) Update(ctx context.Context, id string, expectedVersion uint64, update storage.SubagentUpdate) (storage.Subagent, error) {
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	record, exists := s.records[id]
	if !exists {
		return storage.Subagent{}, fmt.Errorf("%w: %q", storage.ErrSubagentNotFound, id)
	}
	if record.Version != expectedVersion {
		return storage.Subagent{}, fmt.Errorf("%w: %q", storage.ErrSubagentVersionConflict, id)
	}
	if record.Status == storage.SubagentStatusClosed {
		return storage.Subagent{}, fmt.Errorf("%w: %q", storage.ErrSubagentClosed, id)
	}
	if update.Status == storage.SubagentStatusClosed {
		return storage.Subagent{}, fmt.Errorf("%w: use Close", storage.ErrInvalidSubagent)
	}
	record.Status = update.Status
	record.CurrentTurnID = update.CurrentTurnID
	record.LastTurnID = update.LastTurnID
	record.LastTurnError = update.LastTurnError
	record.UpdatedAt = nextSubagentTimestamp(record)
	record.Version++
	if err := storage.ValidateSubagent(record); err != nil {
		return storage.Subagent{}, err
	}
	s.records[id] = record
	return storage.CloneSubagent(record), nil
}

// Enqueue appends an immutable mailbox entry in FIFO order.
func (s *SubagentStorage) Enqueue(ctx context.Context, id string, message storage.SubagentQueuedMessage) (storage.Subagent, error) {
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	if err := storage.ValidateSubagentQueuedMessage(message); err != nil {
		return storage.Subagent{}, fmt.Errorf("%w: queued message: %v", storage.ErrInvalidSubagent, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	record, exists := s.records[id]
	if !exists {
		return storage.Subagent{}, fmt.Errorf("%w: %q", storage.ErrSubagentNotFound, id)
	}
	if record.Status == storage.SubagentStatusClosed {
		return storage.Subagent{}, fmt.Errorf("%w: %q", storage.ErrSubagentClosed, id)
	}
	for _, queued := range record.Pending {
		if queued.ID == message.ID {
			return storage.Subagent{}, fmt.Errorf("%w: %q", storage.ErrDuplicateSubagentMessageID, message.ID)
		}
	}
	record.Pending = append(record.Pending, message)
	record.UpdatedAt = nextSubagentTimestamp(record)
	record.Version++
	s.records[id] = record
	return storage.CloneSubagent(record), nil
}

// Dequeue removes and returns the oldest mailbox entry. An empty queue returns
// a nil message without error.
func (s *SubagentStorage) Dequeue(ctx context.Context, id string) (storage.Subagent, *storage.SubagentQueuedMessage, error) {
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, nil, err
	}
	record, exists := s.records[id]
	if !exists {
		return storage.Subagent{}, nil, fmt.Errorf("%w: %q", storage.ErrSubagentNotFound, id)
	}
	if record.Status == storage.SubagentStatusClosed {
		return storage.Subagent{}, nil, fmt.Errorf("%w: %q", storage.ErrSubagentClosed, id)
	}
	if len(record.Pending) == 0 {
		return storage.CloneSubagent(record), nil, nil
	}
	message := record.Pending[0]
	record.Pending = append([]storage.SubagentQueuedMessage(nil), record.Pending[1:]...)
	record.UpdatedAt = nextSubagentTimestamp(record)
	record.Version++
	s.records[id] = record
	return storage.CloneSubagent(record), &message, nil
}

// Observe advances the parent's durable child-message observation cursor. A
// stale cursor is a successful no-op, which makes concurrent readers safe.
func (s *SubagentStorage) Observe(ctx context.Context, id, messageID string, version uint64) (storage.Subagent, error) {
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	if messageID == "" {
		return storage.Subagent{}, fmt.Errorf("%w: observed message ID is required", storage.ErrInvalidSubagent)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	record, exists := s.records[id]
	if !exists {
		return storage.Subagent{}, fmt.Errorf("%w: %q", storage.ErrSubagentNotFound, id)
	}
	if version <= record.ObservedVersion {
		return storage.CloneSubagent(record), nil
	}
	record.ObservedMessageID = messageID
	record.ObservedVersion = version
	record.UpdatedAt = nextSubagentTimestamp(record)
	record.Version++
	s.records[id] = record
	return storage.CloneSubagent(record), nil
}

// Close idempotently prevents new work, drops pending input, and retains the
// child metadata and transcript cursor for later inspection.
func (s *SubagentStorage) Close(ctx context.Context, id string) (storage.Subagent, error) {
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.Subagent{}, err
	}
	record, exists := s.records[id]
	if !exists {
		return storage.Subagent{}, fmt.Errorf("%w: %q", storage.ErrSubagentNotFound, id)
	}
	if record.Status == storage.SubagentStatusClosed {
		return storage.CloneSubagent(record), nil
	}
	now := nextSubagentTimestamp(record)
	record.Status = storage.SubagentStatusClosed
	record.CurrentTurnID = ""
	record.Pending = nil
	record.UpdatedAt = now
	record.ClosedAt = &now
	record.Version++
	if err := storage.ValidateSubagent(record); err != nil {
		return storage.Subagent{}, err
	}
	s.records[id] = record
	return storage.CloneSubagent(record), nil
}

var _ storage.SubagentStorage = (*SubagentStorage)(nil)

func nextSubagentTimestamp(record storage.Subagent) time.Time {
	now := time.Now().UTC()
	if now.Before(record.UpdatedAt) {
		return record.UpdatedAt
	}
	return now
}
