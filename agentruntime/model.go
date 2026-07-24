package agentruntime

import (
	"context"
	"fmt"

	"github.com/mrbryside/agentcli/provider"
)

// ToolChoiceMode controls whether a provider may choose a tool for a model
// response. Providers map these provider-neutral values to their native
// request shape.
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceSpecific ToolChoiceMode = "specific"
)

// ToolChoice is an optional provider-neutral tool selection instruction. A
// specific choice forces the named tool; the other modes control whether the
// model may call tools without naming one.
type ToolChoice struct {
	Mode ToolChoiceMode
	Name string
}

// Validate checks that the choice can be represented by a provider adapter.
func (choice ToolChoice) Validate() error {
	switch choice.Mode {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		if choice.Name != "" {
			return fmt.Errorf("tool choice mode %q cannot include a tool name", choice.Mode)
		}
	case ToolChoiceSpecific:
		if choice.Name == "" {
			return fmt.Errorf("specific tool choice requires a tool name")
		}
	default:
		return fmt.Errorf("unknown tool choice mode %q", choice.Mode)
	}
	return nil
}

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
	// ToolChoice is optional. A nil value lets the provider choose its
	// default behavior; a specific choice can force one tool during a repair
	// round.
	ToolChoice *ToolChoice
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
