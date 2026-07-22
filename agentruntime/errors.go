package agentruntime

import "errors"

// Sentinel errors returned by the runtime's public operations.
var (
	ErrInvalidRequest    = errors.New("invalid agent request")
	ErrTurnInProgress    = errors.New("turn already in progress")
	ErrTurnExists        = errors.New("turn already exists")
	ErrRunNotFound       = errors.New("run not found")
	ErrRunNotDone        = errors.New("run is not complete")
	ErrRunInterrupted    = errors.New("run interrupted")
	ErrMaxSteps          = errors.New("maximum provider steps reached")
	ErrToolResultsClosed = errors.New("tool results channel closed")
)
