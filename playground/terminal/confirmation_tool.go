package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mrbryside/agentcli"
)

const maximumConfirmationDemoActionLength = 240

type confirmationDemoInput struct {
	Action string `json:"action"`
}

type confirmationDemoOutput struct {
	Status               string `json:"status"`
	Action               string `json:"action"`
	ChangedExternalState bool   `json:"changed_external_state"`
	Message              string `json:"message"`
}

// withConfirmationDemoTool is a harmless example of a custom tool-controlled
// Yes/No gate. Its handler never mutates external state; it runs only after the
// runtime receives a correlated Yes decision.
func withConfirmationDemoTool() agentcli.Option {
	return agentcli.WithTool(newConfirmationDemoTool())
}

func newConfirmationDemoTool() agentcli.Tool {
	return agentcli.Tool{
		Definition: agentcli.ToolDefinition{
			Name:        "confirm_demo",
			Description: "Demonstrate a Yes/No confirmation before a harmless mock action. Use only when the user explicitly asks to test or demonstrate confirmation.",
			InputSchema: agentcli.ObjectSchema(struct{ Action agentcli.ToolParameter }{
				Action: agentcli.StringParameter("Short description of the harmless mock action to show to the user").Required().MinLength(1).MaxLength(maximumConfirmationDemoActionLength),
			}),
		},
		Handler:      executeConfirmationDemo,
		Confirmation: describeConfirmationDemo,
	}
}

func describeConfirmationDemo(arguments json.RawMessage) (agentcli.ToolConfirmationDescription, error) {
	input, err := decodeConfirmationDemo(arguments)
	if err != nil {
		return agentcli.ToolConfirmationDescription{}, err
	}
	input, err = normalizeConfirmationDemoInput(input)
	if err != nil {
		return agentcli.ToolConfirmationDescription{}, err
	}
	return agentcli.ToolConfirmationDescription{
		Title:   "Confirm mock action",
		Message: "Run this harmless mock action?",
		Details: "Action: " + input.Action,
	}, nil
}

func executeConfirmationDemo(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := decodeConfirmationDemo(arguments)
	if err != nil {
		return nil, err
	}
	input, err = normalizeConfirmationDemoInput(input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(confirmationDemoOutput{
		Status:               "completed",
		Action:               input.Action,
		ChangedExternalState: false,
		Message:              "Mock action completed; no external state changed.",
	})
}

func decodeConfirmationDemo(arguments json.RawMessage) (confirmationDemoInput, error) {
	var input confirmationDemoInput
	if err := agentcli.DecodeArguments(arguments, &input); err != nil {
		return confirmationDemoInput{}, err
	}
	return input, nil
}

func normalizeConfirmationDemoInput(input confirmationDemoInput) (confirmationDemoInput, error) {
	// Collapse whitespace so tool-provided text cannot forge extra terminal
	// prompt lines when it is rendered as confirmation details.
	input.Action = strings.Join(strings.Fields(input.Action), " ")
	if input.Action == "" {
		return confirmationDemoInput{}, errors.New("action is required")
	}
	if len(input.Action) > maximumConfirmationDemoActionLength {
		return confirmationDemoInput{}, errors.New("action must be at most 240 characters")
	}
	return input, nil
}
