package inmemory

import (
	"sort"
	"sync"
	"time"

	"github.com/mrbryside/agentcli/confirmation"
)

type ConfirmationStorage struct {
	mu      sync.Mutex
	records map[confirmation.ID]confirmation.Record
}

func NewConfirmationStorage() *ConfirmationStorage {
	return &ConfirmationStorage{records: make(map[confirmation.ID]confirmation.Record)}
}

func (storage *ConfirmationStorage) Create(request confirmation.Request) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := confirmation.ValidateRequest(request); err != nil {
		return err
	}
	if _, exists := storage.records[request.ID]; exists {
		return confirmation.ErrAlreadyResolved
	}
	storage.records[request.ID] = cloneConfirmationRecord(confirmation.Record{Request: request, State: confirmation.Pending})
	return nil
}

func (storage *ConfirmationStorage) Resolve(decision confirmation.Decision) (confirmation.Record, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := confirmation.ValidateDecision(decision); err != nil {
		return confirmation.Record{}, err
	}
	record, exists := storage.records[decision.ConfirmationID]
	if !exists || record.Request.SessionID != decision.SessionID || record.Request.TurnID != decision.TurnID || record.Request.CallID != decision.CallID {
		return confirmation.Record{}, confirmation.ErrNotFound
	}
	if record.State != confirmation.Pending {
		if record.Decision != nil && *record.Decision == decision {
			return cloneConfirmationRecord(record), nil
		}
		return confirmation.Record{}, confirmation.ErrAlreadyResolved
	}
	record.Decision = &decision
	if decision.Answer == confirmation.Yes {
		record.State = confirmation.Confirmed
	} else {
		record.State = confirmation.Declined
	}
	storage.records[decision.ConfirmationID] = record
	return cloneConfirmationRecord(record), nil
}

func (storage *ConfirmationStorage) Cancel(sessionID, turnID string) []confirmation.Record {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	var records []confirmation.Record
	for id, record := range storage.records {
		if record.Request.SessionID == sessionID && record.Request.TurnID == turnID && record.State == confirmation.Pending {
			record.State = confirmation.Cancelled
			storage.records[id] = record
			records = append(records, cloneConfirmationRecord(record))
		}
	}
	sortConfirmationRecords(records)
	return records
}

func (storage *ConfirmationStorage) Get(id confirmation.ID) (confirmation.Record, bool) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	record, exists := storage.records[id]
	return cloneConfirmationRecord(record), exists
}

func (storage *ConfirmationStorage) Pending(sessionID string) []confirmation.Record {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	var records []confirmation.Record
	for _, record := range storage.records {
		if record.State == confirmation.Pending && (sessionID == "" || record.Request.SessionID == sessionID) {
			records = append(records, cloneConfirmationRecord(record))
		}
	}
	sortConfirmationRecords(records)
	return records
}

func (storage *ConfirmationStorage) Expire(now time.Time) []confirmation.Record {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	var records []confirmation.Record
	for id, record := range storage.records {
		if record.State == confirmation.Pending && record.Request.ExpiresAt != nil && !record.Request.ExpiresAt.After(now) {
			record.State = confirmation.Expired
			storage.records[id] = record
			records = append(records, cloneConfirmationRecord(record))
		}
	}
	sortConfirmationRecords(records)
	return records
}

func sortConfirmationRecords(records []confirmation.Record) {
	sort.Slice(records, func(i, j int) bool { return records[i].Request.ID < records[j].Request.ID })
}

func cloneConfirmationRecord(record confirmation.Record) confirmation.Record {
	clone := record
	if record.Request.ExpiresAt != nil {
		expiresAt := *record.Request.ExpiresAt
		clone.Request.ExpiresAt = &expiresAt
	}
	if record.Decision != nil {
		decision := *record.Decision
		clone.Decision = &decision
	}
	return clone
}
