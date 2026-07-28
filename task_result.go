package agentcli

import (
	"encoding/json"

	"github.com/mrbryside/agentcli/agentruntime"
)

// taskDelivery is the Agent-owned handoff for one terminal background task.
// It never leaves the runtime as an application API: consumers receive the
// final main-agent response while applications may separately observe the
// corresponding SystemTaskCompleted event.
type taskDelivery struct {
	MainAgentSessionID string
	MainAgentTurnID    string
	AssignmentID       string
	SubagentSessionID  string
	SubagentTurnID     string
	Result             TaskResult
	Metadata           map[string]any
}

// RuntimeMessage returns the only provider-visible representation of a later
// task completion. Contract metadata is intentionally absent: it is for the
// embedding application and is published only through SystemTaskCompleted.
func (delivery taskDelivery) RuntimeMessage() agentruntime.Message {
	payload, _ := json.Marshal(struct {
		TaskID         string                                     `json:"task_id"`
		Agent          string                                     `json:"agent"`
		State          TaskState                                  `json:"state"`
		Output         string                                     `json:"output"`
		Error          string                                     `json:"error"`
	}{
		TaskID: delivery.Result.TaskID, Agent: delivery.Result.AgentName,
		State: delivery.Result.State, Output: delivery.Result.Output,
		Error: delivery.Result.Error,
	})
	return agentruntime.Message{
		Type:    agentruntime.MessageTypeRuntimeEvent,
		Content: "<task_result>\n" + string(payload) + "\n</task_result>",
	}
}

func taskFinalResultFromOutput(taskID string, definition SubagentDefinition, output string, incomplete bool) (TaskResult, map[string]any) {
	result := TaskResult{TaskID: taskID, AgentName: definition.Name}
	final, err := parseTaskFinalResult(definition, output)
	if err != nil {
		result.State = TaskStateError
		result.Error = err.Error()
		return result, nil
	}
	result.Output = final.Output
	if incomplete {
		result.State = TaskStateIncomplete
	} else {
		result.State = TaskStateCompleted
	}
	return result, cloneTaskMetadata(final.Metadata)
}

func taskResultFromFinalOutput(taskID string, definition SubagentDefinition, output string, incomplete bool) TaskResult {
	result, _ := taskFinalResultFromOutput(taskID, definition, output, incomplete)
	return result
}
