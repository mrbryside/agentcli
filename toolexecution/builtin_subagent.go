package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
)

// TaskToolName is the sole framework tool through which a main agent assigns
// work to, or resumes work by, a configured subagent.
const TaskToolName = "task"

// IsSubagentToolName reports whether name is reserved by the model-facing task
// protocol. It intentionally does not reserve the retired lifecycle tool names:
// applications may still use their host-only subagent session APIs while the
// model can only use task.
func IsSubagentToolName(name string) bool { return name == TaskToolName }

// TaskAgent describes one task target that is safe to show to the main model.
// The caller provides the project-configured catalog that its task executor
// accepts.
type TaskAgent struct {
	Name        string
	Description string
}

// TaskToolInput is the provider-facing flat task input. Pointer fields keep
// validation strict: a resumed task must omit agent and description rather than
// merely provide empty values.
type TaskToolInput struct {
	Agent       *string `json:"agent"`
	Description *string `json:"description"`
	Prompt      string  `json:"prompt"`
	TaskID      *string `json:"task_id"`
	Background  bool    `json:"background"`
}

// TaskToolExecutor is bound by agentcli after its subagent manager exists. It
// deliberately returns raw JSON so this package stays independent of the root
// TaskRequest and TaskResult public types.
type TaskToolExecutor func(context.Context, Invocation, TaskToolInput) (json.RawMessage, error)

// TaskToolBridge allows the task tool to be registered before agentcli creates
// its private subagent manager. Subagents never receive this bridge.
type TaskToolBridge struct {
	mu       sync.RWMutex
	executor TaskToolExecutor
	agents   []TaskAgent
}

// NewTaskToolBridge builds a task catalog for exactly the supplied available
// agents. Empty or duplicate names are omitted and ordering is stable.
func NewTaskToolBridge(agents []TaskAgent) *TaskToolBridge {
	byName := make(map[string]TaskAgent, len(agents))
	for _, agent := range agents {
		agent.Name = strings.TrimSpace(agent.Name)
		agent.Description = strings.TrimSpace(agent.Description)
		if agent.Name == "" || agent.Description == "" {
			continue
		}
		byName[agent.Name] = agent
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	available := make([]TaskAgent, 0, len(names))
	for _, name := range names {
		available = append(available, byName[name])
	}
	return &TaskToolBridge{agents: available}
}

// Bind installs the private main-agent task executor.
func (bridge *TaskToolBridge) Bind(executor TaskToolExecutor) {
	bridge.mu.Lock()
	bridge.executor = executor
	bridge.mu.Unlock()
}

func (bridge *TaskToolBridge) get() (TaskToolExecutor, error) {
	bridge.mu.RLock()
	executor := bridge.executor
	bridge.mu.RUnlock()
	if executor == nil {
		return nil, errors.New("task manager is unavailable")
	}
	return executor, nil
}

// Tools returns the one main-agent-only task built-in.
func (bridge *TaskToolBridge) Tools() []Tool {
	return []Tool{{
		Definition: agentruntime.ToolDefinition{
			Name:        TaskToolName,
			Description: bridge.description(),
			InputSchema: mustRawToolSchema(`{"type":"object","properties":{"agent":{"type":"string","minLength":1,"description":"Exact configured agent name for a new task."},"description":{"type":"string","minLength":1,"description":"Short label describing a new task."},"prompt":{"type":"string","minLength":1,"description":"Complete instructions and context for this task turn."},"task_id":{"type":"string","minLength":1,"description":"Exact ID of an existing resumable task in this main-agent session. A missing or closed ID returns an error and never creates a replacement task."},"background":{"type":"boolean","description":"Run without waiting for the final result. Omit or use false to return the result in this turn."}},"required":["prompt"],"additionalProperties":false}`),
		},
		Handler: bridge.handle,
	}}
}

func (bridge *TaskToolBridge) description() string {
	var description strings.Builder
	description.WriteString("Run one focused task with a configured agent. For a new task, provide agent, description, and prompt. To continue any existing task later, provide its exact task_id and a prompt; agent and description are unnecessary. The presence of task_id always selects resume mode, and the runtime ignores agent or description if they are accidentally included. Completed, incomplete, and failed runs remain resumable. A missing or closed task_id returns error_code task_not_found or task_closed and never creates a replacement task. After any resume error, keep the same task_id when correcting the call; never remove task_id merely to make the call succeed. If a task needs essential user information, ask its exact question, then resume the same task_id with the user's answer. Foreground is the default: its final output is returned by this call. Use background only when later delivery is appropriate. Put independent tasks in the same assistant tool-call message so they can run in parallel. When exactly two independent readers are needed, make exactly two task calls in that one message, one source prompt each, without waiting for the first. Do not call task again merely to check progress or make tool calls solely to delay a response.")
	if len(bridge.agents) == 0 {
		return description.String()
	}
	description.WriteString("\n\nAvailable agents:")
	for _, agent := range bridge.agents {
		fmt.Fprintf(&description, "\n- %s: %s", agent.Name, agent.Description)
	}
	return description.String()
}

func (bridge *TaskToolBridge) handle(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input TaskToolInput
	if err := decodeTaskTool(arguments, &input); err != nil {
		return nil, err
	}
	input = normalizeTaskToolInput(input)
	if err := validateTaskToolInput(input); err != nil {
		return nil, err
	}
	invocation, ok := InvocationFromContext(ctx)
	if !ok || invocation.ToolName != TaskToolName {
		return nil, fmt.Errorf("%s requires tool invocation context", TaskToolName)
	}
	executor, err := bridge.get()
	if err != nil {
		return nil, err
	}
	return executor(ctx, invocation, input)
}

// normalizeTaskToolInput makes task_id the unambiguous mode discriminator.
// Models sometimes repeat the create-only agent and description fields while
// resuming; those fields cannot retarget the retained task and are discarded.
func normalizeTaskToolInput(input TaskToolInput) TaskToolInput {
	if input.TaskID != nil {
		input.Agent = nil
		input.Description = nil
	}
	return input
}

func decodeTaskTool(arguments json.RawMessage, output any) error {
	decoder := json.NewDecoder(strings.NewReader(string(arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode task tool arguments: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode task tool arguments: multiple JSON values")
		}
		return fmt.Errorf("decode task tool arguments: %w", err)
	}
	return nil
}

func validateTaskToolInput(input TaskToolInput) error {
	if strings.TrimSpace(input.Prompt) == "" {
		return errors.New("task prompt is required")
	}
	if input.TaskID == nil {
		if input.Agent == nil || strings.TrimSpace(*input.Agent) == "" {
			return errors.New("task agent is required for a new task")
		}
		if input.Description == nil || strings.TrimSpace(*input.Description) == "" {
			return errors.New("task description is required for a new task")
		}
		return nil
	}
	if strings.TrimSpace(*input.TaskID) == "" {
		return errors.New("task_id cannot be empty")
	}
	return nil
}
