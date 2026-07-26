// Package storage defines the provider-neutral conversation transcript domain.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MessageType identifies the kind of value stored in a conversation transcript.
type MessageType string

const (
	MessageTypeSystem MessageType = "system"
	MessageTypeUser   MessageType = "user"
	// MessageTypeRuntimeEvent is trusted application activity that asks the
	// agent to continue without pretending a human sent another message.
	// Provider adapters choose a provider-legal role for this generic value.
	MessageTypeRuntimeEvent MessageType = "runtime_event"
	MessageTypeAssistant    MessageType = "assistant"
	MessageTypeToolCall     MessageType = "tool_call"
	MessageTypeToolResult   MessageType = "tool_result"
	// MessageTypeCompactionCheckpoint is an internal, append-only cumulative
	// summary used to project a bounded transcript for a future provider run.
	// It is not an ordinary conversation message for a provider to render.
	MessageTypeCompactionCheckpoint MessageType = "compaction_checkpoint"
)

// ToolCall is a provider-neutral request to invoke a tool.
type ToolCall struct {
	CallID    string
	Name      string
	Arguments json.RawMessage
}

// ToolResultStatus identifies how a tool invocation ended.
type ToolResultStatus string

const (
	ToolResultSucceeded   ToolResultStatus = "succeeded"
	ToolResultFailed      ToolResultStatus = "failed"
	ToolResultInterrupted ToolResultStatus = "interrupted"
	ToolResultDenied      ToolResultStatus = "denied"
	ToolResultDeclined    ToolResultStatus = "declined"
)

// ToolResult is the outcome of exactly one tool invocation.
type ToolResult struct {
	CallID string
	Name   string
	Status ToolResultStatus
	Output json.RawMessage
	Error  string
	// TriggerSatisfied overrides the default rule that a succeeded result
	// satisfies a required trigger. Runtime-owned skipped trigger calls set
	// this to false while ordinary tool results leave it nil.
	TriggerSatisfied *bool
}

// CompactionCheckpoint retains the cumulative summary of messages through
// CoversThroughMessageID and identifies the first original message that must
// remain verbatim in the projected tail. The IDs are intentionally retained
// even though the transcript is append-only so a resumed run can reconstruct
// the same projection deterministically.
//
// Storage validates only that these values are present. The runtime validates
// that they name messages in the same transcript and have legal ordering.
type CompactionCheckpoint struct {
	Summary                string
	CoversThroughMessageID string
	TailStartMessageID     string
}

// Message is a provider-neutral value retained in a conversation transcript.
type Message struct {
	ID         string
	SessionID  string
	TurnID     string
	Type       MessageType
	Content    string
	Reasoning  string
	ToolCalls  []ToolCall
	ToolResult *ToolResult
	// CompactionCheckpoint is set only for MessageTypeCompactionCheckpoint.
	CompactionCheckpoint *CompactionCheckpoint
	CreatedAt            time.Time
}

// MessageStorage persists ordered messages for a conversation session.
type MessageStorage interface {
	Append(ctx context.Context, messages ...Message) error
	List(ctx context.Context, sessionID string) ([]Message, error)
	TurnExists(ctx context.Context, sessionID, turnID string) (bool, error)
}

var (
	// ErrInvalidMessage indicates a message does not satisfy the stored-domain
	// invariants. Returned errors wrap this sentinel with field context.
	ErrInvalidMessage = errors.New("invalid message")

	// ErrDuplicateMessageID indicates an attempted append repeats a stored ID.
	ErrDuplicateMessageID = errors.New("duplicate message ID")
)

// ValidateMessage verifies that message is safe to retain in a transcript.
func ValidateMessage(message Message) error {
	if message.ID == "" {
		return invalidMessage("ID is required")
	}
	if message.SessionID == "" {
		return invalidMessage("session ID is required")
	}
	if message.TurnID == "" {
		return invalidMessage("turn ID is required")
	}
	if message.Type != MessageTypeCompactionCheckpoint && message.CompactionCheckpoint != nil {
		return invalidMessage("%s message cannot include a compaction checkpoint", message.Type)
	}

	switch message.Type {
	case MessageTypeSystem, MessageTypeUser, MessageTypeRuntimeEvent:
		if strings.TrimSpace(message.Content) == "" {
			return invalidMessage("%s message requires content", message.Type)
		}
		if len(message.ToolCalls) != 0 {
			return invalidMessage("%s message cannot include tool calls", message.Type)
		}
		if message.ToolResult != nil {
			return invalidMessage("%s message cannot include a tool result", message.Type)
		}
		if message.Reasoning != "" {
			return invalidMessage("%s message cannot include reasoning", message.Type)
		}
	case MessageTypeAssistant:
		if len(message.ToolCalls) != 0 {
			return invalidMessage("%s message cannot include tool calls", message.Type)
		}
		if message.ToolResult != nil {
			return invalidMessage("%s message cannot include a tool result", message.Type)
		}
	case MessageTypeToolCall:
		if message.ToolResult != nil {
			return invalidMessage("tool-call message cannot include a tool result")
		}
		if len(message.ToolCalls) == 0 {
			return invalidMessage("tool-call message requires at least one tool call")
		}
		for index, call := range message.ToolCalls {
			if err := validateToolCall(call); err != nil {
				return invalidMessage("tool call %d: %v", index, err)
			}
		}
	case MessageTypeToolResult:
		if message.Content != "" {
			return invalidMessage("tool-result message cannot include content")
		}
		if message.Reasoning != "" {
			return invalidMessage("tool-result message cannot include reasoning")
		}
		if len(message.ToolCalls) != 0 {
			return invalidMessage("tool-result message cannot include tool calls")
		}
		if message.ToolResult == nil {
			return invalidMessage("tool-result message requires a tool result")
		}
		if err := validateToolResult(*message.ToolResult); err != nil {
			return invalidMessage("tool result: %v", err)
		}
	case MessageTypeCompactionCheckpoint:
		if message.Content != "" {
			return invalidMessage("compaction-checkpoint message cannot include content")
		}
		if message.Reasoning != "" {
			return invalidMessage("compaction-checkpoint message cannot include reasoning")
		}
		if len(message.ToolCalls) != 0 {
			return invalidMessage("compaction-checkpoint message cannot include tool calls")
		}
		if message.ToolResult != nil {
			return invalidMessage("compaction-checkpoint message cannot include a tool result")
		}
		if err := validateCompactionCheckpoint(message.CompactionCheckpoint); err != nil {
			return invalidMessage("compaction checkpoint: %v", err)
		}
	default:
		return invalidMessage("unknown type %q", message.Type)
	}

	return nil
}

func validateCompactionCheckpoint(checkpoint *CompactionCheckpoint) error {
	if checkpoint == nil {
		return errors.New("is required")
	}
	if strings.TrimSpace(checkpoint.Summary) == "" {
		return errors.New("summary is required")
	}
	if strings.TrimSpace(checkpoint.CoversThroughMessageID) == "" {
		return errors.New("covers-through message ID is required")
	}
	if strings.TrimSpace(checkpoint.TailStartMessageID) == "" {
		return errors.New("tail-start message ID is required")
	}
	return nil
}

func validateToolCall(call ToolCall) error {
	if call.CallID == "" {
		return errors.New("call ID is required")
	}
	if call.Name == "" {
		return errors.New("name is required")
	}
	if !json.Valid(call.Arguments) {
		return errors.New("arguments must be valid JSON")
	}
	return nil
}

func validateToolResult(result ToolResult) error {
	if result.CallID == "" {
		return errors.New("call ID is required")
	}
	if result.Name == "" {
		return errors.New("name is required")
	}
	switch result.Status {
	case ToolResultSucceeded:
		if result.Error != "" {
			return errors.New("succeeded result cannot include an error")
		}
		if !json.Valid(result.Output) {
			return errors.New("succeeded result output must be valid JSON")
		}
	case ToolResultFailed, ToolResultInterrupted, ToolResultDenied, ToolResultDeclined:
		if result.Error == "" {
			return errors.New("failed or interrupted result requires an error")
		}
		if len(result.Output) != 0 && !json.Valid(result.Output) {
			return errors.New("output must be valid JSON")
		}
	default:
		return fmt.Errorf("unknown status %q", result.Status)
	}
	if result.TriggerSatisfied != nil && *result.TriggerSatisfied && result.Status != ToolResultSucceeded {
		return errors.New("only a succeeded result can satisfy a trigger")
	}
	return nil
}

func invalidMessage(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidMessage}, args...)...)
}
