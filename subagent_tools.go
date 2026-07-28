package agentcli

import "github.com/mrbryside/agentcli/toolexecution"

// TaskToolName is the only subagent execution tool visible to the main model.
const TaskToolName = toolexecution.TaskToolName

var subagentToolNames = map[string]struct{}{
	TaskToolName: {},
}

func isSubagentToolName(name string) bool { return toolexecution.IsSubagentToolName(name) }
