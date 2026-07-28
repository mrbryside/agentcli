package agentcli

// TaskState describes the runtime-owned outcome of a model-facing task.
type TaskState string

const (
	TaskStateRunning    TaskState = "running"
	TaskStateCompleted  TaskState = "completed"
	TaskStateIncomplete TaskState = "incomplete"
	TaskStateError      TaskState = "error"
)

// TaskResult is the model-facing result of a task execution.
type TaskResult struct {
	TaskID    string    `json:"task_id"`
	AgentName string    `json:"agent"`
	State     TaskState `json:"state"`
	Output    string    `json:"output"`
	Error     string    `json:"error"`
}
