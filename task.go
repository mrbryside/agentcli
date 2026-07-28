package agentcli

// TaskState describes the runtime-owned outcome of a model-facing task.
type TaskState string
type TaskErrorCode string

const (
	TaskStateRunning    TaskState = "running"
	TaskStateCompleted  TaskState = "completed"
	TaskStateIncomplete TaskState = "incomplete"
	TaskStateError      TaskState = "error"

	TaskErrorNotFound TaskErrorCode = "task_not_found"
	TaskErrorClosed   TaskErrorCode = "task_closed"
	TaskErrorRunning  TaskErrorCode = "task_running"
)

// TaskResult is the model-facing result of a task execution.
type TaskResult struct {
	TaskID    string        `json:"task_id"`
	AgentName string        `json:"agent"`
	State     TaskState     `json:"state"`
	Output    string        `json:"output"`
	ErrorCode TaskErrorCode `json:"error_code,omitempty"`
	Error     string        `json:"error"`
}
