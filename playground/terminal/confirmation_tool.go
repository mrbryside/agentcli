package main

import (
	"context"
	"errors"
	"strings"

	"harness-api/agentcli"
	"harness-api/confirmation"
	"harness-api/toolexecution"
)

const maximumConfirmationDemoActionLength = 240

type confirmationDemoInput struct {
	Action string `json:"action" description:"Short description of the harmless mock action to show to the user" minLength:"1" maxLength:"240"`
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
	return agentcli.WithCustomTool(
		"confirm_demo",
		"Demonstrate a Yes/No confirmation before a harmless mock action. Use only when the user explicitly asks to test or demonstrate confirmation.",
		executeConfirmationDemo,
		agentcli.ToolConfirmation(describeConfirmationDemo),
	)
}

func newConfirmationDemoTool() (toolexecution.Tool, error) {
	return agentcli.NewCustomTool(
		"confirm_demo",
		"Demonstrate a Yes/No confirmation before a harmless mock action. Use only when the user explicitly asks to test or demonstrate confirmation.",
		executeConfirmationDemo,
		agentcli.ToolConfirmation(describeConfirmationDemo),
	)
}

func describeConfirmationDemo(input confirmationDemoInput) (confirmation.Description, error) {
	input, err := normalizeConfirmationDemoInput(input)
	if err != nil {
		return confirmation.Description{}, err
	}
	return confirmation.Description{
		Title:   "Confirm mock action",
		Message: "Run this harmless mock action?",
		Details: "Action: " + input.Action,
	}, nil
}

func executeConfirmationDemo(ctx context.Context, input confirmationDemoInput) (confirmationDemoOutput, error) {
	if err := ctx.Err(); err != nil {
		return confirmationDemoOutput{}, err
	}
	input, err := normalizeConfirmationDemoInput(input)
	if err != nil {
		return confirmationDemoOutput{}, err
	}
	return confirmationDemoOutput{
		Status:               "completed",
		Action:               input.Action,
		ChangedExternalState: false,
		Message:              "Mock action completed; no external state changed.",
	}, nil
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
