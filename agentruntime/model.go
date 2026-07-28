package agentruntime

import (
	"context"
	"errors"

	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
)

// ErrContextWindowExceeded marks a provider rejection caused by the complete
// request exceeding the selected model's context window. Model adapters should
// wrap this sentinel when they can identify that condition so the runtime can
// force one provider-neutral compaction retry.
var ErrContextWindowExceeded = errors.New("model context window exceeded")

// ModelRequest is the provider-neutral input for one provider stream round.
type ModelRequest struct {
	SessionID string
	TurnID    string
	// MaxOutputTokens optionally constrains generated output for this request.
	// Zero leaves the provider/model default in effect.
	MaxOutputTokens int
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

// Clone returns an independent copy of request. It is useful to model and
// context-estimation implementations that retain a request after a call.
func (request ModelRequest) Clone() ModelRequest {
	clone := request
	clone.SystemPrompts = append([]string(nil), request.SystemPrompts...)
	clone.ContextReminders = cloneContextReminders(request.ContextReminders)
	clone.Messages = storage.CloneMessages(request.Messages)
	clone.Tools = cloneToolDefinitions(request.Tools)
	return clone
}

// CloneModelRequest returns an independent copy of request.
func CloneModelRequest(request ModelRequest) ModelRequest {
	return request.Clone()
}

// ContextReminderRequest identifies the provider round whose transient
// context is being resolved.
type ContextReminderRequest struct {
	SessionID    string
	TurnID       string
	ProviderStep int
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

// ModelIdentity describes the configured provider and model used by a Model.
// It is observability metadata only and does not affect provider behavior.
type ModelIdentity struct {
	Provider string
	Model    string
}

// ModelIdentityProvider is an optional model capability used by
// observability decorators to label provider calls without depending on a
// provider-specific adapter.
type ModelIdentityProvider interface {
	ModelIdentity() ModelIdentity
}

// ModelMetadataProvider is an optional model capability. Runtime consumers
// use it when a feature, such as context compaction, needs known model limits.
// Keeping it separate from Model preserves compatibility with models that do
// not expose model metadata.
type ModelMetadataProvider interface {
	ModelMetadata() (ModelMetadata, error)
}

// ContextEstimatorProvider is an optional model capability for selecting the
// token estimator that matches the main model's provider. Runtime uses it when
// compaction is enabled and no explicit estimator was configured. Returning a
// nil estimator falls back to GenericContextEstimator.
type ContextEstimatorProvider interface {
	ContextEstimator() ContextEstimator
}
