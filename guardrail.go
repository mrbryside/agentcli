package agentcli

import "github.com/mrbryside/agentcli/agentruntime"

// InputGuard and OutputGuard are the application callbacks accepted by the
// corresponding Agent construction options.
type InputGuard = agentruntime.InputGuard
type InputGuardAttempt = agentruntime.InputGuardAttempt
type InputGuardDecision = agentruntime.InputGuardDecision
type InputGuardAction = agentruntime.InputGuardAction

type OutputGuard = agentruntime.OutputGuard
type OutputGuardAttempt = agentruntime.OutputGuardAttempt
type OutputGuardDecision = agentruntime.OutputGuardDecision
type OutputGuardAction = agentruntime.OutputGuardAction

const (
	InputAccept  = agentruntime.InputAccept
	InputReplace = agentruntime.InputReplace
	InputReject  = agentruntime.InputReject

	OutputProceed = agentruntime.OutputProceed
	OutputRetry   = agentruntime.OutputRetry
)

// ErrInputGuardRejected identifies input that was rejected before a Run was
// created or the message was persisted.
var ErrInputGuardRejected = agentruntime.ErrInputGuardRejected
