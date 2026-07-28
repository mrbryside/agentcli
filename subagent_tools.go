package agentcli

import "github.com/mrbryside/agentcli/toolexecution"

// TaskToolName is the only subagent execution tool visible to the main model.
const TaskToolName = toolexecution.TaskToolName

var subagentToolNames = map[string]struct{}{
	TaskToolName: {},
}

func isSubagentToolName(name string) bool { return toolexecution.IsSubagentToolName(name) }

// Transitional aliases keep pre-task terminal and integration code compiling
// until Tasks 8 and 10 migrate their callers. They are never registered in a
// model catalog and must be removed before the v0.1.0 release.
//
// TODO(tasks-8-10): delete after host callers no longer reference them.
const (
	StartSubagentToolName       = "start_subagent"
	ListSubagentsToolName       = "list_subagents"
	SubagentStatusToolName      = "subagent_status"
	SendSubagentMessageToolName = "send_subagent_message"
)
