package agentruntime

import (
	"context"

	"github.com/mrbryside/agentcli/provider"
)

// ModelRequest is the provider-neutral input for one provider stream round.
type ModelRequest struct {
	SessionID string
	TurnID    string
	// SystemPrompts are application-owned instruction messages supplied
	// separately from the persisted conversation transcript.
	SystemPrompts []string
	// ContextReminders are trusted, ephemeral runtime context. They are never
	// persisted in the conversation transcript; each model adapter chooses a
	// provider-legal placement for them.
	ContextReminders []ContextReminder
	Messages         []Message
	Tools            []ToolDefinition
}

// ContextReminder is trusted runtime context supplied alongside, but outside
// of, the persisted conversation transcript.
type ContextReminder struct {
	Content string
}

// ContextReminderRequest identifies the provider round whose transient
// context is being resolved.
type ContextReminderRequest struct {
	SessionID string
	TurnID    string
}

// ContextReminderProvider resolves trusted transient context for one provider
// round. Implementations must not mutate the stored transcript.
type ContextReminderProvider func(context.Context, ContextReminderRequest) ([]ContextReminder, error)

// ModelStream is the provider stream interface consumed by Runtime.
type ModelStream interface {
	Subscribe(context.Context) <-chan provider.StreamEvent
	Result() (provider.StreamResult, error)
}

// Model starts one provider-neutral streaming round.
type Model interface {
	Start(context.Context, ModelRequest) (ModelStream, error)
}
