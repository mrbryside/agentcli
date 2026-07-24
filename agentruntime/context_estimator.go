package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidContextEstimate indicates an estimator returned a value that
// cannot be used for context budgeting.
var ErrInvalidContextEstimate = errors.New("invalid context estimate")

// ContextEstimate is the estimated input size of a provider request. Tokens
// excludes generated output tokens.
type ContextEstimate struct {
	Tokens int
}

// Validate verifies that the estimate can be used in a context budget.
func (estimate ContextEstimate) Validate() error {
	if estimate.Tokens < 0 {
		return fmt.Errorf("%w: tokens cannot be negative", ErrInvalidContextEstimate)
	}
	return nil
}

// ContextEstimator estimates the input context occupied by a generic model
// request. Implementations must not mutate request or retain its mutable
// members without first cloning them.
type ContextEstimator interface {
	Estimate(ModelRequest) (ContextEstimate, error)
}

// ContextEstimatorFunc adapts a function to ContextEstimator.
type ContextEstimatorFunc func(ModelRequest) (ContextEstimate, error)

// Estimate implements ContextEstimator.
func (function ContextEstimatorFunc) Estimate(request ModelRequest) (ContextEstimate, error) {
	if function == nil {
		return ContextEstimate{}, errors.New("context estimator function is nil")
	}
	estimate, err := function(request.Clone())
	if err != nil {
		return ContextEstimate{}, err
	}
	if err := estimate.Validate(); err != nil {
		return ContextEstimate{}, err
	}
	return estimate, nil
}

// GenericContextEstimator is a deterministic, provider-neutral estimator.
// It charges ASCII at a four-characters-per-token approximation while charging
// every non-ASCII UTF-8 byte as a token. This deliberately overestimates
// multilingual text, where a byte-only four-to-one approximation could
// otherwise undercount Thai, CJK, and emoji. Fixed framing costs cover message
// and tool protocol structures. Its zero value is ready to use.
// Provider-specific implementations can replace it when exact sizing is
// available.
type GenericContextEstimator struct{}

const (
	genericCharactersPerToken = 4
	genericMessageOverhead    = 4
	genericPromptOverhead     = 4
	genericReminderOverhead   = 8
	genericToolOverhead       = 12
)

// Estimate implements ContextEstimator.
func (GenericContextEstimator) Estimate(request ModelRequest) (ContextEstimate, error) {
	// Validate schemas before cloning. ToolSchema.Clone intentionally returns an
	// empty schema when its source cannot be encoded, which is appropriate for
	// defensive copying elsewhere but would hide an invalid estimate input.
	for index, tool := range request.Tools {
		if _, err := json.Marshal(tool.InputSchema); err != nil {
			return ContextEstimate{}, fmt.Errorf("estimate tool %d schema: %w", index, err)
		}
	}
	// Clone once at the boundary so callers can safely reuse or mutate their
	// request after Estimate returns, and future estimator changes cannot leak
	// aliases through mutable schema or JSON values.
	request = request.Clone()

	tokens := 0
	for _, prompt := range request.SystemPrompts {
		tokens += genericPromptOverhead + genericTextTokens(prompt)
	}
	for _, reminder := range request.ContextReminders {
		tokens += genericReminderOverhead + genericTextTokens(reminder.Content)
	}
	for _, message := range request.Messages {
		tokens += genericMessageOverhead + genericTextTokens(string(message.Type))
		tokens += genericTextTokens(message.Content) + genericTextTokens(message.Reasoning)
		for _, call := range message.ToolCalls {
			tokens += genericTextTokens(call.CallID) + genericTextTokens(call.Name)
			tokens += genericBytesTokens(call.Arguments)
		}
		if message.ToolResult != nil {
			result := message.ToolResult
			tokens += genericTextTokens(result.CallID) + genericTextTokens(result.Name)
			tokens += genericTextTokens(string(result.Status)) + genericTextTokens(result.Error)
			tokens += genericBytesTokens(result.Output)
		}
	}
	for index, tool := range request.Tools {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return ContextEstimate{}, fmt.Errorf("estimate tool %d schema: %w", index, err)
		}
		tokens += genericToolOverhead + genericTextTokens(tool.Name) + genericTextTokens(tool.Description)
		tokens += genericBytesTokens(schema)
	}

	estimate := ContextEstimate{Tokens: tokens}
	if err := estimate.Validate(); err != nil {
		return ContextEstimate{}, err
	}
	return estimate, nil
}

func genericTextTokens(text string) int {
	return genericBytesTokens([]byte(text))
}

func genericBytesTokens(value []byte) int {
	var asciiBytes, nonASCIIBytes int
	for _, byteValue := range value {
		if byteValue < 0x80 {
			asciiBytes++
		} else {
			nonASCIIBytes++
		}
	}
	return (asciiBytes+genericCharactersPerToken-1)/genericCharactersPerToken + nonASCIIBytes
}
