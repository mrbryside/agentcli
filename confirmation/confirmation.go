// Package confirmation defines provider-neutral Yes/No gates requested by a
// custom tool before it executes. Confirmations are intentionally independent
// from permission policy and permission modes.
package confirmation

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ID string

type Answer string

const (
	Yes Answer = "yes"
	No  Answer = "no"
)

type State string

const (
	Pending   State = "pending"
	Confirmed State = "confirmed"
	Declined  State = "declined"
	Cancelled State = "cancelled"
	Expired   State = "expired"
)

// Description is supplied by a custom tool for one invocation. Message is
// the primary question; Details provides the information users should review.
type Description struct {
	Title   string
	Message string
	Details string
}

type Request struct {
	ID                        ID
	SessionID, TurnID, CallID string
	ToolName                  string
	Title, Message, Details   string
	CreatedAt                 time.Time
	ExpiresAt                 *time.Time
}

type Decision struct {
	ConfirmationID            ID
	SessionID, TurnID, CallID string
	Answer                    Answer
}

type Record struct {
	Request  Request
	State    State
	Decision *Decision
}

var (
	ErrInvalid         = errors.New("invalid confirmation")
	ErrNotFound        = errors.New("confirmation not found")
	ErrAlreadyResolved = errors.New("confirmation already resolved")
	ErrClosed          = errors.New("confirmation closed")
)

func NewID() (ID, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate confirmation ID: %w", err)
	}
	return ID("confirm_" + hex.EncodeToString(bytes)), nil
}

func ValidateDescription(description Description) error {
	if strings.TrimSpace(description.Message) == "" {
		return fmt.Errorf("%w: message is required", ErrInvalid)
	}
	return nil
}

func ValidateRequest(request Request) error {
	if request.ID == "" || request.SessionID == "" || request.TurnID == "" || request.CallID == "" || request.ToolName == "" || strings.TrimSpace(request.Message) == "" || request.CreatedAt.IsZero() {
		return fmt.Errorf("%w: required identity, tool, message, and timestamp", ErrInvalid)
	}
	if request.ExpiresAt != nil && !request.ExpiresAt.After(request.CreatedAt) {
		return fmt.Errorf("%w: expiry must follow creation", ErrInvalid)
	}
	return nil
}

func ValidateDecision(decision Decision) error {
	if decision.ConfirmationID == "" || decision.SessionID == "" || decision.TurnID == "" || decision.CallID == "" {
		return fmt.Errorf("%w: missing decision correlation", ErrInvalid)
	}
	if decision.Answer != Yes && decision.Answer != No {
		return fmt.Errorf("%w: answer must be yes or no", ErrInvalid)
	}
	return nil
}
