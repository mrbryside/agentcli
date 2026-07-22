package storage

import (
	"harness-api/permission"
	"time"
)

// PermissionStorage persists permission lifecycle state independently of the transcript.
type PermissionStorage interface {
	Create(permission.Request) error
	Resolve(permission.Decision) (permission.Record, error)
	Cancel(sessionID, turnID string) []permission.Record
	Get(permission.ID) (permission.Record, bool)
	Pending(sessionID string) []permission.Record
	Expire(time.Time) []permission.Record
}
