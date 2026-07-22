package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// ErrStreamNotDone indicates that a result was requested before a terminal
// event was processed.
var ErrStreamNotDone = errors.New("stream is not complete")

// Result folds the event history into a StreamResult.
func Result(events []StreamEvent) (StreamResult, error) {
	content := ""
	reasoning := ""
	pending := make(map[int]*toolAccumulator)
	completed := make([]ToolCall, 0)
	finished := false

	for _, event := range events {
		switch event.Type {
		case ContentReceived:
			content += event.Content
		case ReasoningReceived:
			reasoning += event.Reasoning
		case ToolCallStarted:
			if event.Tool == nil {
				return StreamResult{}, errors.New("tool_call_started event has no tool")
			}
			tool := event.Tool
			builder := pending[tool.Index]
			if builder == nil {
				builder = &toolAccumulator{}
				pending[tool.Index] = builder
			}
			builder.ID = firstNonEmpty(tool.ID, builder.ID)
			builder.Type = firstNonEmpty(tool.Type, builder.Type)
			builder.Name = firstNonEmpty(tool.Name, builder.Name)
		case ToolArgumentsReceived:
			if event.Tool == nil {
				return StreamResult{}, errors.New("tool_arguments_received event has no tool")
			}
			builder := pending[event.Tool.Index]
			if builder == nil {
				builder = &toolAccumulator{}
				pending[event.Tool.Index] = builder
			}
			builder.RawArgs += event.Tool.Arguments
		case ToolCallCompleted:
			if event.Tool == nil {
				return StreamResult{}, errors.New("tool_call_completed event has no tool")
			}
			tool, err := completeTool(pending, event.Tool.Index)
			if err != nil {
				return StreamResult{}, err
			}
			completed = append(completed, tool)
		case StreamCompleted:
			if len(pending) > 0 {
				return StreamResult{}, fmt.Errorf("%d tool call(s) were not completed", len(pending))
			}
			finished = true
		case StreamFailed:
			return StreamResult{}, streamFailure(event)
		}
	}

	if !finished {
		return StreamResult{}, ErrStreamNotDone
	}

	return StreamResult{
		Content:        content,
		Reasoning:      reasoning,
		CompletedTools: completed,
		Finished:       true,
	}, nil
}

type toolAccumulator struct {
	ID      string
	Type    string
	Name    string
	RawArgs string
}

func completeTool(pending map[int]*toolAccumulator, index int) (ToolCall, error) {
	builder, ok := pending[index]
	if !ok {
		return ToolCall{}, fmt.Errorf("tool call index %d was not started", index)
	}
	delete(pending, index)

	arguments, err := parseArguments(builder.RawArgs)
	if err != nil {
		return ToolCall{}, fmt.Errorf("tool %q arguments: %w", builder.Name, err)
	}
	if builder.Name == "" {
		return ToolCall{}, errors.New("tool call name is required")
	}

	return ToolCall{
		ID:        builder.ID,
		Type:      builder.Type,
		Name:      builder.Name,
		Arguments: arguments,
	}, nil
}

func parseArguments(raw string) (map[string]any, error) {
	if raw == "" {
		return map[string]any{}, nil
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
		return nil, err
	}
	if arguments == nil {
		return map[string]any{}, nil
	}
	return arguments, nil
}

func streamFailure(event StreamEvent) error {
	if event.Error != nil {
		return event.Error
	}
	if payload, ok := event.Payload.(StreamFailedPayload); ok && payload.Error != nil {
		return payload.Error
	}
	if payload, ok := event.Payload.(*StreamFailedPayload); ok && payload != nil && payload.Error != nil {
		return payload.Error
	}
	return errors.New("stream failed")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func pendingToolCompletionEvents(events []StreamEvent) ([]StreamEvent, error) {
	pending := make(map[int]*toolAccumulator)
	for _, event := range events {
		switch event.Type {
		case ToolCallStarted:
			if event.Tool == nil {
				return nil, errors.New("tool_call_started event has no tool")
			}
			builder := pending[event.Tool.Index]
			if builder == nil {
				builder = &toolAccumulator{}
				pending[event.Tool.Index] = builder
			}
			builder.ID = firstNonEmpty(event.Tool.ID, builder.ID)
			builder.Type = firstNonEmpty(event.Tool.Type, builder.Type)
			builder.Name = firstNonEmpty(event.Tool.Name, builder.Name)
		case ToolArgumentsReceived:
			if event.Tool == nil {
				return nil, errors.New("tool_arguments_received event has no tool")
			}
			builder := pending[event.Tool.Index]
			if builder == nil {
				builder = &toolAccumulator{}
				pending[event.Tool.Index] = builder
			}
			builder.RawArgs += event.Tool.Arguments
		case ToolCallCompleted:
			if event.Tool == nil {
				return nil, errors.New("tool_call_completed event has no tool")
			}
			delete(pending, event.Tool.Index)
		}
	}

	indexes := make([]int, 0, len(pending))
	for index := range pending {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	completed := make([]StreamEvent, 0, len(indexes))
	for _, index := range indexes {
		builder := pending[index]
		completed = append(completed, StreamEvent{
			Type: ToolCallCompleted,
			Tool: &ToolEvent{
				Index: index,
				ID:    builder.ID,
				Type:  builder.Type,
				Name:  builder.Name,
			},
		})
	}
	return completed, nil
}
