package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SubagentStatus describes active or terminal lifecycle state. An empty status
// means the retained task session is not running and may be resumed.
type SubagentStatus string

const (
	SubagentStatusRunning SubagentStatus = "running"
	SubagentStatusClosed  SubagentStatus = "closed"
)

// SubagentResultStatus is the semantic result of the most recently finished
// subagent turn. It is independent from the running/closed lifecycle.
type SubagentResultStatus string

const (
	SubagentResultCompleted  SubagentResultStatus = "completed"
	SubagentResultIncomplete SubagentResultStatus = "incomplete"
	SubagentResultFailed     SubagentResultStatus = "failed"
)

// SubagentQueuedMessage is one ordered main agent-to-subagent mailbox entry.
type SubagentQueuedMessage struct {
	ID        string
	Content   string
	CreatedAt time.Time
}

// TaskDelivery identifies the main-agent turn and assignment that should
// receive a later task result. It is separate from Subagent.MainAgentTurnID,
// which always records the turn that originally created the subagent session.
type TaskDelivery struct {
	MainAgentTurnID string
	AssignmentID    string
}

// Subagent is provider-neutral state for one subagent instance. Subagent
// transcript messages remain in MessageStorage under SubagentSessionID.
type Subagent struct {
	ID                 string
	DisplayName        string
	Label              string
	MainAgentSessionID string
	MainAgentTurnID    string
	SubagentSessionID  string
	DefinitionName     string
	Provider           string
	Model              string

	Status                SubagentStatus
	CurrentSubagentTurnID string
	LastSubagentTurnID    string
	LastResultError       string
	LastResultStatus      SubagentResultStatus
	LastResultSummary     string
	LastResultNextStep    string
	ActiveTaskDelivery    *TaskDelivery
	Version               uint64

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
	Status                SubagentStatus
	CurrentSubagentTurnID string
	LastSubagentTurnID    string
	LastResultError       string
	LastResultStatus      SubagentResultStatus
	LastResultSummary     string
	LastResultNextStep    string
	ActiveTaskDelivery    *TaskDelivery
}

// SubagentStorage persists main-agent-to-subagent session relationships independently of
// provider messages and events. Every returned record is a defensive copy.
type SubagentStorage interface {
	Create(context.Context, Subagent) (Subagent, error)
	Get(ctx context.Context, id string) (Subagent, bool, error)
	ListByMainAgent(ctx context.Context, mainAgentSessionID string) ([]Subagent, error)
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
	// ErrSubagentNotFound indicates the requested subagent instance is absent.
	ErrSubagentNotFound = errors.New("subagent not found")
	// ErrSubagentVersionConflict indicates a compare-safe mutation lost a race.
	ErrSubagentVersionConflict = errors.New("subagent version conflict")
	// ErrSubagentClosed indicates an operation would send work to a closed subagent.
	ErrSubagentClosed = errors.New("subagent closed")
	// ErrSubagentRunning indicates an operation requires no active subagent turn.
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
		{"main agent session ID", subagent.MainAgentSessionID},
		{"main agent turn ID", subagent.MainAgentTurnID},
		{"subagent session ID", subagent.SubagentSessionID},
		{"definition name", subagent.DefinitionName},
		{"provider", subagent.Provider},
		{"model", subagent.Model},
	} {
		if field.value == "" {
			return invalidSubagent("%s is required", field.name)
		}
	}
	if subagent.SubagentSessionID == subagent.MainAgentSessionID {
		return invalidSubagent("subagent session ID must differ from main agent session ID")
	}
	if subagent.CreatedAt.IsZero() || subagent.UpdatedAt.IsZero() {
		return invalidSubagent("created and updated timestamps are required")
	}
	if subagent.UpdatedAt.Before(subagent.CreatedAt) {
		return invalidSubagent("updated timestamp precedes created timestamp")
	}

	switch subagent.Status {
	case "":
		if subagent.CurrentSubagentTurnID != "" {
			return invalidSubagent("resumable subagent cannot have a current subagent turn ID")
		}
		if subagent.ClosedAt != nil {
			return invalidSubagent("resumable subagent cannot have a closed timestamp")
		}
	case SubagentStatusRunning:
		if subagent.CurrentSubagentTurnID == "" {
			return invalidSubagent("running subagent requires a current subagent turn ID")
		}
		if subagent.ClosedAt != nil {
			return invalidSubagent("running subagent cannot have a closed timestamp")
		}
	case SubagentStatusClosed:
		if subagent.CurrentSubagentTurnID != "" {
			return invalidSubagent("closed subagent cannot have a current subagent turn ID")
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
	if subagent.LastResultStatus != "" && subagent.LastSubagentTurnID == "" {
		return invalidSubagent("last result status requires a last subagent turn ID")
	}
	if subagent.LastResultError != "" && subagent.LastResultStatus != SubagentResultFailed {
		return invalidSubagent("last result error requires failed status")
	}
	switch subagent.LastResultStatus {
	case "":
		if subagent.LastResultSummary != "" || subagent.LastResultNextStep != "" {
			return invalidSubagent("last result details require a result status")
		}
	case SubagentResultCompleted:
		if subagent.LastResultSummary == "" {
			return invalidSubagent("completed result requires a summary")
		}
		if subagent.LastResultNextStep != "" {
			return invalidSubagent("completed result cannot require a next step")
		}
	case SubagentResultIncomplete:
		if subagent.LastResultSummary == "" || subagent.LastResultNextStep == "" {
			return invalidSubagent("incomplete result requires a summary and next step")
		}
	case SubagentResultFailed:
		if subagent.LastResultError == "" {
			return invalidSubagent("failed result requires an error")
		}
	default:
		return invalidSubagent("unknown last result status %q", subagent.LastResultStatus)
	}
	if err := ValidateTaskDelivery(subagent.ActiveTaskDelivery); err != nil {
		return invalidSubagent("active task delivery: %v", err)
	}
	return nil
}

// ValidateTaskDelivery verifies an optional identity for later task-result
// delivery. Both identifiers are required whenever a delivery is present.
func ValidateTaskDelivery(delivery *TaskDelivery) error {
	if delivery == nil {
		return nil
	}
	if delivery.MainAgentTurnID == "" {
		return errors.New("main agent turn ID is required")
	}
	if delivery.AssignmentID == "" {
		return errors.New("assignment ID is required")
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

// CloneSubagent returns a defensive copy of a subagent record.
func CloneSubagent(subagent Subagent) Subagent {
	clone := subagent
	if subagent.Pending != nil {
		clone.Pending = append([]SubagentQueuedMessage(nil), subagent.Pending...)
	}
	if subagent.ClosedAt != nil {
		closedAt := *subagent.ClosedAt
		clone.ClosedAt = &closedAt
	}
	clone.ActiveTaskDelivery = CloneTaskDelivery(subagent.ActiveTaskDelivery)
	return clone
}

// CloneTaskDelivery returns a defensive copy of an optional task delivery.
func CloneTaskDelivery(delivery *TaskDelivery) *TaskDelivery {
	if delivery == nil {
		return nil
	}
	clone := *delivery
	return &clone
}

// CloneSubagents returns defensive copies of subagent records.
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
