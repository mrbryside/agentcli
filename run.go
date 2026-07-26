package agentcli

import (
	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/toolexecution"
)

// Run and its event types are exposed at the root package so ordinary
// applications can start turns and consume their streams without importing
// the lower-level runtime package.
type Run = agentruntime.Run
type RunResult = agentruntime.RunResult
type RunStatus = agentruntime.RunStatus
type AgentEvent = agentruntime.AgentEvent
type EventType = agentruntime.EventType
type EventCursor = agentruntime.EventCursor
type EventSubscription = agentruntime.EventSubscription

// ScopeEventType identifies PreEndScope or EndScope.
type ScopeEventType = toolexecution.ScopeEventType

// ScopeEvent describes one live response-scope boundary.
type ScopeEvent = toolexecution.ScopeEvent

const (
	RunStarted                 = agentruntime.RunStarted
	ProviderEventReceived      = agentruntime.ProviderEventReceived
	ToolCallRequested          = agentruntime.ToolCallRequested
	ToolResultReceived         = agentruntime.ToolResultReceived
	RunCompleted               = agentruntime.RunCompleted
	RunFailed                  = agentruntime.RunFailed
	AgentInterrupted           = agentruntime.AgentInterrupted
	AgentPermissionRequested   = agentruntime.AgentPermissionRequested
	AgentPermissionResolved    = agentruntime.AgentPermissionResolved
	AgentPermissionCancelled   = agentruntime.AgentPermissionCancelled
	AgentPermissionExpired     = agentruntime.AgentPermissionExpired
	AgentConfirmationRequested = agentruntime.AgentConfirmationRequested
	AgentConfirmationResolved  = agentruntime.AgentConfirmationResolved
	AgentConfirmationCancelled = agentruntime.AgentConfirmationCancelled
	AgentConfirmationExpired   = agentruntime.AgentConfirmationExpired
	PermissionModeChanged      = agentruntime.PermissionModeChanged

	// PreEndScope is emitted when a response scope becomes quiescent, before
	// cleanup and final EndResponseScope tool handlers run.
	PreEndScope = toolexecution.PreEndScope
	// EndScope is emitted after cleanup and final EndResponseScope tool
	// handlers finish and the scope is removed.
	EndScope = toolexecution.EndScope

	RunStatusActive                 = agentruntime.RunStatusActive
	RunStatusWaitingForPermission   = agentruntime.RunStatusWaitingForPermission
	RunStatusWaitingForConfirmation = agentruntime.RunStatusWaitingForConfirmation
	RunStatusDone                   = agentruntime.RunStatusDone
)

// Provider event aliases make the common content-streaming path available
// through the root package as well.
type ProviderEvent = provider.StreamEvent
type ProviderEventType = provider.EventType

const (
	ContentReceived       = provider.ContentReceived
	ReasoningReceived     = provider.ReasoningReceived
	ToolCallStarted       = provider.ToolCallStarted
	ToolArgumentsReceived = provider.ToolArgumentsReceived
	ToolCallCompleted     = provider.ToolCallCompleted
	StreamCompleted       = provider.StreamCompleted
	StreamFailed          = provider.StreamFailed
)

// Runtime errors are re-exported so direct Agent callers can classify failures
// without importing agentruntime.
var (
	ErrInvalidRequest    = agentruntime.ErrInvalidRequest
	ErrTurnInProgress    = agentruntime.ErrTurnInProgress
	ErrTurnExists        = agentruntime.ErrTurnExists
	ErrRunNotFound       = agentruntime.ErrRunNotFound
	ErrRunNotDone        = agentruntime.ErrRunNotDone
	ErrRunInterrupted    = agentruntime.ErrRunInterrupted
	ErrMaxSteps          = agentruntime.ErrMaxSteps
	ErrToolResultsClosed = agentruntime.ErrToolResultsClosed
)
