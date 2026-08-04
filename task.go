package agentcli

// TaskState describes the runtime-owned outcome of a model-facing task.
type TaskState string
type taskErrorCode string

const (
	TaskStateRunning    TaskState = "running"
	TaskStateCompleted  TaskState = "completed"
	TaskStateIncomplete TaskState = "incomplete"
	TaskStateError      TaskState = "error"

	taskErrorNotFound taskErrorCode = "task_not_found"
	taskErrorClosed   taskErrorCode = "task_closed"
	taskErrorRunning  taskErrorCode = "task_running"
)

// taskResult is the model-facing result of a task execution.
type taskResult struct {
	TaskID    string        `json:"task_id"`
	AgentName string        `json:"agent"`
	State     TaskState     `json:"state"`
	Output    string        `json:"output"`
	ErrorCode taskErrorCode `json:"error_code,omitempty"`
	Error     string        `json:"error"`
}
