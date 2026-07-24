package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mrbryside/agentcli/provider"
)

// InputGuard inspects a request before it is persisted or sent to the model.
// The attempt is a defensive copy and may be retained by the caller.
type InputGuard func(context.Context, InputGuardAttempt) (InputGuardDecision, error)

// OutputGuard inspects the latest model output before the runtime completes a
// turn. A retry decision sends Feedback to the next provider round.
type OutputGuard func(context.Context, OutputGuardAttempt) (OutputGuardDecision, error)

type InputGuardAttempt struct {
	SessionID string
	TurnID    string
	Message   Message
}

type InputGuardAction string

const (
	InputAccept  InputGuardAction = "accept"
	InputReplace InputGuardAction = "replace"
	InputReject  InputGuardAction = "reject"
)

type InputGuardDecision struct {
	Action  InputGuardAction
	Message *Message
	Reason  string
}

type OutputGuardAttempt struct {
	SessionID     string
	TurnID        string
	Messages      []Message
	Output        Message
	ProviderSteps int
	RetryCount    int
}

type OutputGuardAction string

const (
	OutputProceed OutputGuardAction = "proceed"
	OutputRetry   OutputGuardAction = "retry"
)

type OutputGuardDecision struct {
	Action   OutputGuardAction
	Feedback string
}

func invokeInputGuard(ctx context.Context, guard InputGuard, attempt InputGuardAttempt) (decision InputGuardDecision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("guard panicked: %v", recovered)
		}
	}()
	return guard(ctx, attempt)
}

func invokeOutputGuard(ctx context.Context, guard OutputGuard, attempt OutputGuardAttempt) (decision OutputGuardDecision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("guard panicked: %v", recovered)
		}
	}()
	return guard(ctx, attempt)
}

// ToolOutputGuard inspects one successful custom-tool handler output before
// the executor publishes it to the runtime. Rejecting it produces a failed
// tool result with Feedback so the agent can decide whether to call the tool
// again.
type ToolOutputGuard func(context.Context, ToolOutputGuardAttempt) (ToolOutputGuardDecision, error)

type ToolOutputGuardAttempt struct {
	SessionID string
	TurnID    string
	CallID    string
	ToolName  string
	Arguments json.RawMessage
	Output    json.RawMessage
}

type ToolOutputGuardAction string

const (
	ToolOutputProceed ToolOutputGuardAction = "proceed"
	ToolOutputReject  ToolOutputGuardAction = "reject"
)

type ToolOutputGuardDecision struct {
	Action   ToolOutputGuardAction
	Feedback string
}

// Validate checks that the tool-output decision can be translated into one
// unambiguous tool result.
func (decision ToolOutputGuardDecision) Validate() error {
	switch decision.Action {
	case ToolOutputProceed:
		if strings.TrimSpace(decision.Feedback) != "" {
			return errors.New("proceed tool-output decision cannot include feedback")
		}
	case ToolOutputReject:
		if strings.TrimSpace(decision.Feedback) == "" {
			return errors.New("reject tool-output decision requires feedback")
		}
	default:
		return fmt.Errorf("unknown tool-output guard action %q", decision.Action)
	}
	return nil
}

func validateInputGuardDecision(decision InputGuardDecision, sessionID, turnID string) error {
	switch decision.Action {
	case InputAccept:
		if decision.Message != nil || strings.TrimSpace(decision.Reason) != "" {
			return errors.New("accept input decision cannot include a replacement or reason")
		}
	case InputReplace:
		if decision.Message == nil {
			return errors.New("replace input decision requires a message")
		}
		if decision.Message.SessionID != "" && decision.Message.SessionID != sessionID {
			return errors.New("replacement message changes the session ID")
		}
		if decision.Message.TurnID != "" && decision.Message.TurnID != turnID {
			return errors.New("replacement message changes the turn ID")
		}
	case InputReject:
		if decision.Message != nil {
			return errors.New("reject input decision cannot include a replacement")
		}
		if strings.TrimSpace(decision.Reason) == "" {
			return errors.New("reject input decision requires a reason")
		}
	default:
		return fmt.Errorf("unknown input guard action %q", decision.Action)
	}
	return nil
}

func validateOutputGuardDecision(decision OutputGuardDecision) error {
	switch decision.Action {
	case OutputProceed:
		if strings.TrimSpace(decision.Feedback) != "" {
			return errors.New("proceed output decision cannot include feedback")
		}
	case OutputRetry:
		if strings.TrimSpace(decision.Feedback) == "" {
			return errors.New("retry output decision requires feedback")
		}
	default:
		return fmt.Errorf("unknown output guard action %q", decision.Action)
	}
	return nil
}

type promptGuardVerdict struct {
	Allowed  *bool   `json:"allowed"`
	Reason   *string `json:"reason"`
	Feedback *string `json:"feedback"`
}

func newPromptInputGuard(model Model, prompt string) InputGuard {
	return func(ctx context.Context, attempt InputGuardAttempt) (InputGuardDecision, error) {
		verdict, err := evaluatePromptGuard(ctx, model, prompt, "input", attempt.Message)
		if err != nil {
			return InputGuardDecision{}, err
		}
		if *verdict.Allowed {
			return InputGuardDecision{Action: InputAccept}, nil
		}
		reason := strings.TrimSpace(*verdict.Reason)
		if reason == "" {
			reason = "input rejected by prompt guard"
		}
		return InputGuardDecision{Action: InputReject, Reason: reason}, nil
	}
}

func newPromptOutputGuard(model Model, prompt string) OutputGuard {
	return func(ctx context.Context, attempt OutputGuardAttempt) (OutputGuardDecision, error) {
		verdict, err := evaluatePromptGuard(ctx, model, prompt, "output", attempt.Output)
		if err != nil {
			return OutputGuardDecision{}, err
		}
		if *verdict.Allowed {
			return OutputGuardDecision{Action: OutputProceed}, nil
		}
		feedback := strings.TrimSpace(*verdict.Feedback)
		if feedback == "" {
			feedback = strings.TrimSpace(*verdict.Reason)
		}
		if feedback == "" {
			feedback = "The previous output did not satisfy the output guard. Produce a compliant answer."
		}
		return OutputGuardDecision{Action: OutputRetry, Feedback: feedback}, nil
	}
}

// NewPromptToolOutputGuard creates a tool-output guard backed by one-shot
// model checks. The request exposes no tools and requires a strict JSON
// verdict. Most callers set Tool.ToolOutputGuardPrompt and let Executor build
// this guard with its configured model.
func NewPromptToolOutputGuard(model Model, prompt string) ToolOutputGuard {
	return func(ctx context.Context, attempt ToolOutputGuardAttempt) (ToolOutputGuardDecision, error) {
		payload := struct {
			ToolName  string          `json:"tool_name"`
			Arguments json.RawMessage `json:"arguments"`
			Output    json.RawMessage `json:"output"`
		}{
			ToolName:  attempt.ToolName,
			Arguments: cloneRawJSON(attempt.Arguments),
			Output:    cloneRawJSON(attempt.Output),
		}
		verdict, err := evaluatePromptGuard(ctx, model, prompt, "tool output", payload)
		if err != nil {
			return ToolOutputGuardDecision{}, err
		}
		if *verdict.Allowed {
			return ToolOutputGuardDecision{Action: ToolOutputProceed}, nil
		}
		feedback := strings.TrimSpace(*verdict.Feedback)
		if feedback == "" {
			feedback = strings.TrimSpace(*verdict.Reason)
		}
		if feedback == "" {
			feedback = "The tool output did not satisfy its guard. Call the tool again with corrected arguments."
		}
		return ToolOutputGuardDecision{Action: ToolOutputReject, Feedback: feedback}, nil
	}
}

func evaluatePromptGuard(ctx context.Context, model Model, prompt, direction string, value any) (promptGuardVerdict, error) {
	if isNil(model) {
		return promptGuardVerdict{}, errors.New("prompt guard model is nil")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return promptGuardVerdict{}, fmt.Errorf("encode %s for prompt guard: %w", direction, err)
	}
	request := ModelRequest{
		SystemPrompts: []string{fmt.Sprintf(
			"You are the %s guard for an agent. Apply the policy below. Return one JSON object only, with exactly these fields: allowed (boolean), reason (string), feedback (string). Always include all three fields. If allowed is true, feedback must be empty. If allowed is false, feedback must tell the agent how to produce a compliant result without repeating unsafe content.\n\nPolicy:\n%s",
			direction, strings.TrimSpace(prompt),
		)},
		Messages:   []Message{{Type: MessageTypeUser, Content: string(encoded)}},
		ToolChoice: &ToolChoice{Mode: ToolChoiceNone},
	}
	stream, err := model.Start(ctx, request)
	if err != nil {
		return promptGuardVerdict{}, fmt.Errorf("start %s prompt guard: %w", direction, err)
	}
	if stream == nil {
		return promptGuardVerdict{}, fmt.Errorf("start %s prompt guard: model returned a nil stream", direction)
	}

	var content strings.Builder
	var terminal provider.StreamResult
	var hasTerminal bool
	for event := range stream.Subscribe(ctx) {
		switch event.Type {
		case provider.ContentReceived:
			content.WriteString(event.Content)
		case provider.StreamFailed:
			return promptGuardVerdict{}, fmt.Errorf("%s prompt guard stream failed: %w", direction, providerEventError(event))
		case provider.StreamCompleted:
			terminal, hasTerminal = terminalProviderResult(event)
		}
	}
	if err := ctx.Err(); err != nil {
		return promptGuardVerdict{}, err
	}
	if !hasTerminal {
		var resultErr error
		terminal, resultErr = stream.Result()
		if resultErr != nil {
			return promptGuardVerdict{}, fmt.Errorf("read %s prompt guard result: %w", direction, resultErr)
		}
	}
	if strings.TrimSpace(terminal.Content) == "" {
		terminal.Content = content.String()
	}
	var verdict promptGuardVerdict
	if err := decodePromptGuardVerdict(terminal.Content, &verdict); err != nil {
		return promptGuardVerdict{}, fmt.Errorf("decode %s prompt guard result: %w", direction, err)
	}
	return verdict, nil
}

func decodePromptGuardVerdict(content string, verdict *promptGuardVerdict) error {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		if !strings.HasSuffix(content, "```") {
			return errors.New("unterminated JSON fence")
		}
		content = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(content, "```json"), "```"))
	} else if strings.HasPrefix(content, "```") {
		if !strings.HasSuffix(content, "```") {
			return errors.New("unterminated JSON fence")
		}
		content = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(content, "```"), "```"))
	}
	if content == "" {
		return errors.New("empty response")
	}
	if !strings.HasPrefix(content, "{") || !strings.HasSuffix(content, "}") {
		return errors.New("response is not a JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(verdict); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response contains more than one JSON value")
		}
		return err
	}
	if verdict.Allowed == nil {
		return errors.New("response requires allowed")
	}
	if verdict.Reason == nil {
		return errors.New("response requires reason")
	}
	if verdict.Feedback == nil {
		return errors.New("response requires feedback")
	}
	if *verdict.Allowed && strings.TrimSpace(*verdict.Feedback) != "" {
		return errors.New("allowed response cannot include feedback")
	}
	return nil
}
