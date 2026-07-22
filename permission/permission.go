// Package permission contains provider-neutral, caller-approved capabilities.
package permission

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"
)

type ID string
type Action string

const (
	FilesystemRead  Action = "filesystem.read"
	FilesystemWrite Action = "filesystem.write"
	ProcessExecute  Action = "process.execute"
	NetworkAccess   Action = "network.access"
	SandboxBypass   Action = "sandbox.bypass"
)

type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

type State string

const (
	Pending   State = "pending"
	Allowed   State = "allowed"
	Denied    State = "denied"
	Cancelled State = "cancelled"
	Expired   State = "expired"
	Consumed  State = "consumed"
)

type DecisionType string

const (
	AllowOnce    DecisionType = "allow_once"
	AllowSession DecisionType = "allow_session"
	AllowProject DecisionType = "allow_project"
	Deny         DecisionType = "deny"
)

type Mode string

const (
	Default      Mode = "default"
	AcceptEdits  Mode = "acceptEdits"
	CriticalOnly Mode = "criticalOnly"
	DontAsk      Mode = "dontAsk"
	Plan         Mode = "plan"
	Unrestricted Mode = "unrestricted"
)

type Outcome string

const (
	OutcomeAllow Outcome = "allow"
	OutcomeAsk   Outcome = "ask"
	OutcomeDeny  Outcome = "deny"
)

type Request struct {
	ID                        ID
	SessionID, TurnID, CallID string
	ToolName, Details, Reason string
	Risk                      Risk
	Actions                   []Action
	CreatedAt                 time.Time
	ExpiresAt                 *time.Time
}
type Description struct {
	Actions []Action
	Risk    Risk
	Details string
	Reason  string
}
type Decision struct {
	PermissionID              ID
	SessionID, TurnID, CallID string
	Type                      DecisionType
}
type Record struct {
	Request  Request
	State    State
	Decision *Decision
}

var (
	ErrInvalid         = errors.New("invalid permission")
	ErrNotFound        = errors.New("permission not found")
	ErrAlreadyResolved = errors.New("permission already resolved")
	ErrClosed          = errors.New("permission closed")
)

func NewID() (ID, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return ID("perm_" + hex.EncodeToString(b)), nil
}
func ValidateRequest(r Request) error {
	if r.ID == "" || r.SessionID == "" || r.TurnID == "" || r.CallID == "" || r.ToolName == "" || len(r.Actions) == 0 || r.CreatedAt.IsZero() {
		return fmt.Errorf("%w: required identity, tool, and actions", ErrInvalid)
	}
	switch r.Risk {
	case RiskLow, RiskMedium, RiskHigh:
	default:
		return fmt.Errorf("%w: invalid risk", ErrInvalid)
	}
	for _, action := range r.Actions {
		switch action {
		case FilesystemRead, FilesystemWrite, ProcessExecute, NetworkAccess, SandboxBypass:
		default:
			return fmt.Errorf("%w: invalid action", ErrInvalid)
		}
	}
	if r.ExpiresAt != nil && !r.ExpiresAt.After(r.CreatedAt) {
		return fmt.Errorf("%w: expiry must be after creation", ErrInvalid)
	}
	return nil
}
func ValidateDecision(d Decision) error {
	if d.PermissionID == "" || d.SessionID == "" || d.TurnID == "" || d.CallID == "" {
		return fmt.Errorf("%w: missing decision correlation", ErrInvalid)
	}
	switch d.Type {
	case AllowOnce, AllowSession, AllowProject, Deny:
		return nil
	}
	return fmt.Errorf("%w: unknown decision %q", ErrInvalid, d.Type)
}

type Rule struct {
	Actions []Action
	Outcome Outcome
}
type Policy struct {
	Mode  Mode
	Rules []Rule
}

// IsValidMode reports whether mode is one of the permission modes understood
// by Evaluate. It is shared by configuration and live mode changes.
func IsValidMode(mode Mode) bool {
	switch mode {
	case Default, AcceptEdits, CriticalOnly, DontAsk, Plan, Unrestricted:
		return true
	default:
		return false
	}
}

func Evaluate(r Request, p Policy) Outcome {
	for _, rule := range p.Rules {
		if rule.Outcome == OutcomeDeny && matches(r.Actions, rule.Actions) {
			return OutcomeDeny
		}
	}
	for _, rule := range p.Rules {
		if rule.Outcome == OutcomeAsk && matches(r.Actions, rule.Actions) {
			return OutcomeAsk
		}
	}
	for _, rule := range p.Rules {
		if rule.Outcome == OutcomeAllow && covers(rule.Actions, r.Actions) {
			return OutcomeAllow
		}
	}
	switch p.Mode {
	case Unrestricted:
		return OutcomeAllow
	case DontAsk, Plan:
		return OutcomeDeny
	case CriticalOnly:
		if r.Risk == RiskHigh {
			return OutcomeAsk
		}
		return OutcomeAllow
	case AcceptEdits:
		if onlyWrite(r.Actions) {
			return OutcomeAllow
		}
		return OutcomeAsk
	default:
		return OutcomeAsk
	}
}
func matches(actions, wanted []Action) bool {
	if len(wanted) == 0 {
		return false
	}
	for _, a := range actions {
		if slices.Contains(wanted, a) {
			return true
		}
	}
	return false
}
func covers(allowed, requested []Action) bool {
	if len(requested) == 0 {
		return false
	}
	for _, action := range requested {
		if !slices.Contains(allowed, action) {
			return false
		}
	}
	return true
}
func onlyWrite(actions []Action) bool {
	return len(actions) > 0 && func() bool {
		for _, a := range actions {
			if a != FilesystemWrite {
				return false
			}
		}
		return true
	}()
}
