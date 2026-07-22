package inmemory

import (
	"harness-api/permission"
	"sort"
	"sync"
	"time"
)

type PermissionStorage struct {
	mu      sync.Mutex
	records map[permission.ID]permission.Record
}

func NewPermissionStorage() *PermissionStorage {
	return &PermissionStorage{records: map[permission.ID]permission.Record{}}
}
func (m *PermissionStorage) Create(request permission.Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := permission.ValidateRequest(request); err != nil {
		return err
	}
	if _, ok := m.records[request.ID]; ok {
		return permission.ErrAlreadyResolved
	}
	m.records[request.ID] = clonePermissionRecord(permission.Record{Request: request, State: permission.Pending})
	return nil
}
func (m *PermissionStorage) Resolve(decision permission.Decision) (permission.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := permission.ValidateDecision(decision); err != nil {
		return permission.Record{}, err
	}
	record, ok := m.records[decision.PermissionID]
	if !ok {
		return permission.Record{}, permission.ErrNotFound
	}
	if record.Request.SessionID != decision.SessionID || record.Request.TurnID != decision.TurnID || record.Request.CallID != decision.CallID {
		return permission.Record{}, permission.ErrNotFound
	}
	if record.State != permission.Pending {
		if record.Decision != nil && *record.Decision == decision {
			return clonePermissionRecord(record), nil
		}
		return permission.Record{}, permission.ErrAlreadyResolved
	}
	record.Decision = &decision
	if decision.Type == permission.Deny {
		record.State = permission.Denied
	} else {
		record.State = permission.Allowed
	}
	m.records[decision.PermissionID] = record
	return clonePermissionRecord(record), nil
}
func (m *PermissionStorage) Cancel(session, turn string) []permission.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []permission.Record
	for id, record := range m.records {
		if record.Request.SessionID == session && record.Request.TurnID == turn && record.State == permission.Pending {
			record.State = permission.Cancelled
			m.records[id] = record
			out = append(out, clonePermissionRecord(record))
		}
	}
	sortPermissionRecords(out)
	return out
}
func (m *PermissionStorage) Get(id permission.ID) (permission.Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	return clonePermissionRecord(record), ok
}
func (m *PermissionStorage) Pending(session string) []permission.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []permission.Record
	for _, record := range m.records {
		if record.State == permission.Pending && (session == "" || record.Request.SessionID == session) {
			out = append(out, clonePermissionRecord(record))
		}
	}
	sortPermissionRecords(out)
	return out
}
func (m *PermissionStorage) Expire(now time.Time) []permission.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []permission.Record
	for id, record := range m.records {
		if record.State == permission.Pending && record.Request.ExpiresAt != nil && !record.Request.ExpiresAt.After(now) {
			record.State = permission.Expired
			m.records[id] = record
			out = append(out, clonePermissionRecord(record))
		}
	}
	sortPermissionRecords(out)
	return out
}
func sortPermissionRecords(records []permission.Record) {
	sort.Slice(records, func(i, j int) bool { return string(records[i].Request.ID) < string(records[j].Request.ID) })
}
func clonePermissionRecord(record permission.Record) permission.Record {
	clone := record
	clone.Request.Actions = append([]permission.Action(nil), record.Request.Actions...)
	if record.Request.ExpiresAt != nil {
		value := *record.Request.ExpiresAt
		clone.Request.ExpiresAt = &value
	}
	if record.Decision != nil {
		decision := *record.Decision
		clone.Decision = &decision
	}
	return clone
}
