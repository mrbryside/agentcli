package agentruntime

import (
	"errors"
	"fmt"
)

// ErrInvalidModelMetadata indicates a model context capability is incomplete
// or internally inconsistent.
var ErrInvalidModelMetadata = errors.New("invalid model metadata")

// ModelMetadata describes provider-neutral limits for a particular model.
//
// ContextWindowTokens must be known before a caller can safely budget a
// request. MaxOutputTokens is optional: zero means that the provider or model
// does not expose a reliable maximum output limit.
type ModelMetadata struct {
	ContextWindowTokens int
	MaxOutputTokens     int
}

// Validate verifies that metadata is safe to use for context budgeting.
func (metadata ModelMetadata) Validate() error {
	if metadata.ContextWindowTokens <= 0 {
		return invalidModelMetadata("context window tokens must be positive")
	}
	if metadata.MaxOutputTokens < 0 {
		return invalidModelMetadata("maximum output tokens cannot be negative")
	}
	if metadata.MaxOutputTokens > metadata.ContextWindowTokens {
		return invalidModelMetadata("maximum output tokens cannot exceed context window tokens")
	}
	return nil
}

// HasOutputLimit reports whether the model exposes a known output limit.
func (metadata ModelMetadata) HasOutputLimit() bool {
	return metadata.MaxOutputTokens > 0
}

func invalidModelMetadata(format string, arguments ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidModelMetadata}, arguments...)...)
}
