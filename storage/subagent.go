package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SubagentStatus describes the lifecycle state of a child agent session.
type SubagentStatus string

const (
	SubagentStatusRunning SubagentStatus = "running"
	SubagentStatusIdle    SubagentStatus = "idle"
	SubagentStatusClosed  SubagentStatus = "closed"
)

// SubagentTurnOutcome is the semantic result of the most recently finished
// child turn. It is independent from the running/idle/closed lifecycle.
type SubagentTurnOutcome string

const (
	SubagentTurnCompleted  SubagentTurnOutcome = "completed"
	SubagentTurnIncomplete SubagentTurnOutcome = "incomplete"
	SubagentTurnFailed     SubagentTurnOutcome = "failed"
)

// SubagentQueuedMessage is one ordered parent-to-child mailbox entry.
type SubagentQueuedMessage struct {
	ID        string
	Content   string
	CreatedAt time.Time
}

// Subagent is provider-neutral state for one child agent instance. Child
// transcript messages remain in MessageStorage under SessionID.
type Subagent struct {
	ID              string
	DisplayName     string
	Label           string
	ParentSessionID string
	ParentTurnID    string
	SessionID       string
	DefinitionName  string
	Provider        string
	Model           string

	Status           SubagentStatus
	CurrentTurnID    string
	LastTurnID       string
	LastTurnError    string
	LastTurnOutcome  SubagentTurnOutcome
	LastTurnSummary  string
	LastTurnNextStep string
	Version          uint64

	Pending []SubagentQueuedMessage

	ObservedMessageID string
	ObservedVersion   uint64

	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
}

// SubagentUpdate contains the lifecycle values that may be compare-safely
// changed together. Mailbox and observation updates have dedicated methods.
type SubagentUpdate struct {
	Status           SubagentStatus
	CurrentTurnID    string
	LastTurnID       string
	LastTurnError    string
	LastTurnOutcome  SubagentTurnOutcome
	LastTurnSummary  string
	LastTurnNextStep string
}

// SubagentStorage persists parent-child session relationships independently of
// provider messages and events. Every returned record is a defensive copy.
type SubagentStorage interface {
	Create(context.Context, Subagent) (Subagent, error)
	Get(ctx context.Context, id string) (Subagent, bool, error)
	ListByParent(ctx context.Context, parentSessionID string) ([]Subagent, error)
	Update(ctx context.Context, id string, expectedVersion uint64, update SubagentUpdate) (Subagent, error)
	Enqueue(ctx context.Context, id string, message SubagentQueuedMessage) (Subagent, error)
	Dequeue(ctx context.Context, id string) (Subagent, *SubagentQueuedMessage, error)
	Observe(ctx context.Context, id, messageID string, version uint64) (Subagent, error)
	Close(ctx context.Context, id string) (Subagent, error)
}

var (
	// ErrInvalidSubagent indicates a record or mailbox entry violates the
	// provider-neutral storage invariants.
	ErrInvalidSubagent = errors.New("invalid subagent")
	// ErrDuplicateSubagentID indicates Create was called with an existing ID.
	ErrDuplicateSubagentID = errors.New("duplicate subagent ID")
	// ErrDuplicateSubagentMessageID indicates a mailbox repeats an entry ID.
	ErrDuplicateSubagentMessageID = errors.New("duplicate subagent message ID")
	// ErrSubagentNotFound indicates the requested child instance is absent.
	ErrSubagentNotFound = errors.New("subagent not found")
	// ErrSubagentVersionConflict indicates a compare-safe mutation lost a race.
	ErrSubagentVersionConflict = errors.New("subagent version conflict")
	// ErrSubagentClosed indicates an operation would send work to a closed child.
	ErrSubagentClosed = errors.New("subagent closed")
	// ErrSubagentRunning indicates an operation requires the child to be idle.
	ErrSubagentRunning = errors.New("subagent is still running")
)

// ValidateSubagent verifies a record can be retained by a SubagentStorage.
func ValidateSubagent(subagent Subagent) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"ID", subagent.ID},
		{"display name", subagent.DisplayName},
		{"parent session ID", subagent.ParentSessionID},
		{"parent turn ID", subagent.ParentTurnID},
		{"child session ID", subagent.SessionID},
		{"definition name", subagent.DefinitionName},
		{"provider", subagent.Provider},
		{"model", subagent.Model},
	} {
		if field.value == "" {
			return invalidSubagent("%s is required", field.name)
		}
	}
	if subagent.SessionID == subagent.ParentSessionID {
		return invalidSubagent("child session ID must differ from parent session ID")
	}
	if subagent.CreatedAt.IsZero() || subagent.UpdatedAt.IsZero() {
		return invalidSubagent("created and updated timestamps are required")
	}
	if subagent.UpdatedAt.Before(subagent.CreatedAt) {
		return invalidSubagent("updated timestamp precedes created timestamp")
	}

	switch subagent.Status {
	case SubagentStatusRunning:
		if subagent.CurrentTurnID == "" {
			return invalidSubagent("running subagent requires a current turn ID")
		}
		if subagent.ClosedAt != nil {
			return invalidSubagent("running subagent cannot have a closed timestamp")
		}
	case SubagentStatusIdle:
		if subagent.CurrentTurnID != "" {
			return invalidSubagent("idle subagent cannot have a current turn ID")
		}
		if subagent.ClosedAt != nil {
			return invalidSubagent("idle subagent cannot have a closed timestamp")
		}
	case SubagentStatusClosed:
		if subagent.CurrentTurnID != "" {
			return invalidSubagent("closed subagent cannot have a current turn ID")
		}
		if subagent.ClosedAt == nil || subagent.ClosedAt.IsZero() {
			return invalidSubagent("closed subagent requires a closed timestamp")
		}
		if subagent.ClosedAt.Before(subagent.CreatedAt) {
			return invalidSubagent("closed timestamp precedes created timestamp")
		}
	default:
		return invalidSubagent("unknown status %q", subagent.Status)
	}

	queuedIDs := make(map[string]struct{}, len(subagent.Pending))
	for index, message := range subagent.Pending {
		if err := ValidateSubagentQueuedMessage(message); err != nil {
			return invalidSubagent("pending message %d: %v", index, err)
		}
		if _, exists := queuedIDs[message.ID]; exists {
			return invalidSubagent("pending message %d: duplicate ID %q", index, message.ID)
		}
		queuedIDs[message.ID] = struct{}{}
	}
	if subagent.Status == SubagentStatusClosed && len(subagent.Pending) != 0 {
		return invalidSubagent("closed subagent cannot retain pending messages")
	}
	if subagent.ObservedMessageID == "" && subagent.ObservedVersion != 0 {
		return invalidSubagent("observed version requires an observed message ID")
	}
	if subagent.LastTurnOutcome != "" && subagent.LastTurnID == "" {
		return invalidSubagent("last turn outcome requires a last turn ID")
	}
	if subagent.LastTurnError != "" && subagent.LastTurnOutcome != SubagentTurnFailed {
		return invalidSubagent("last turn error requires failed outcome")
	}
	switch subagent.LastTurnOutcome {
	case "":
		if subagent.LastTurnSummary != "" || subagent.LastTurnNextStep != "" {
			return invalidSubagent("last turn details require an outcome")
		}
	case SubagentTurnCompleted:
		if subagent.LastTurnSummary == "" {
			return invalidSubagent("completed last turn requires a summary")
		}
		if subagent.LastTurnNextStep != "" {
			return invalidSubagent("completed last turn cannot require a next step")
		}
	case SubagentTurnIncomplete:
		if subagent.LastTurnSummary == "" || subagent.LastTurnNextStep == "" {
			return invalidSubagent("incomplete last turn requires a summary and next step")
		}
	case SubagentTurnFailed:
		if subagent.LastTurnError == "" {
			return invalidSubagent("failed last turn requires an error")
		}
	default:
		return invalidSubagent("unknown last turn outcome %q", subagent.LastTurnOutcome)
	}
	return nil
}

// ValidateSubagentQueuedMessage verifies one mailbox entry.
func ValidateSubagentQueuedMessage(message SubagentQueuedMessage) error {
	if message.ID == "" {
		return errors.New("ID is required")
	}
	if message.Content == "" {
		return errors.New("content is required")
	}
	if message.CreatedAt.IsZero() {
		return errors.New("created timestamp is required")
	}
	return nil
}

// CloneSubagent returns a defensive copy of a child record.
func CloneSubagent(subagent Subagent) Subagent {
	clone := subagent
	if subagent.Pending != nil {
		clone.Pending = append([]SubagentQueuedMessage(nil), subagent.Pending...)
	}
	if subagent.ClosedAt != nil {
		closedAt := *subagent.ClosedAt
		clone.ClosedAt = &closedAt
	}
	return clone
}

// CloneSubagents returns defensive copies of child records.
func CloneSubagents(subagents []Subagent) []Subagent {
	if subagents == nil {
		return nil
	}
	clones := make([]Subagent, len(subagents))
	for index, subagent := range subagents {
		clones[index] = CloneSubagent(subagent)
	}
	return clones
}

func invalidSubagent(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidSubagent}, args...)...)
}
