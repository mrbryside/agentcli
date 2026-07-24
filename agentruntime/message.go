// Package agentruntime coordinates provider-neutral agent turns.
package agentruntime

import "github.com/mrbryside/agentcli/storage"

// Message and its related types are re-exported from storage so callers can
// use the runtime without importing both packages for ordinary requests.
type (
	Message          = storage.Message
	MessageType      = storage.MessageType
	ToolCall         = storage.ToolCall
	ToolResult       = storage.ToolResult
	ToolResultStatus = storage.ToolResultStatus
)

const (
	MessageTypeSystem       = storage.MessageTypeSystem
	MessageTypeUser         = storage.MessageTypeUser
	MessageTypeRuntimeEvent = storage.MessageTypeRuntimeEvent
	MessageTypeAssistant    = storage.MessageTypeAssistant
	MessageTypeToolCall     = storage.MessageTypeToolCall
	MessageTypeToolResult   = storage.MessageTypeToolResult
	// MessageTypeCompactionCheckpoint is an internal, append-only summary
	// record used to project bounded provider context on later turns.
	MessageTypeCompactionCheckpoint = storage.MessageTypeCompactionCheckpoint

	ToolResultSucceeded   = storage.ToolResultSucceeded
	ToolResultFailed      = storage.ToolResultFailed
	ToolResultInterrupted = storage.ToolResultInterrupted
	ToolResultDenied      = storage.ToolResultDenied
	ToolResultDeclined    = storage.ToolResultDeclined
)
