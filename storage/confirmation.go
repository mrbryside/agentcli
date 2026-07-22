package storage

import (
	"time"

	"github.com/mrbryside/agentcli/confirmation"
)

// ConfirmationStorage persists Yes/No confirmation lifecycle state
// independently from permissions and conversation messages.
type ConfirmationStorage interface {
	Create(confirmation.Request) error
	Resolve(confirmation.Decision) (confirmation.Record, error)
	Cancel(sessionID, turnID string) []confirmation.Record
	Get(confirmation.ID) (confirmation.Record, bool)
	Pending(sessionID string) []confirmation.Record
	Expire(time.Time) []confirmation.Record
}
