package agentcli

import "github.com/mrbryside/agentcli/toolexecution"

// taskToolName is the only subagent execution tool visible to the main model.
const taskToolName = toolexecution.TaskToolName

var subagentToolNames = map[string]struct{}{
	taskToolName: {},
}

func isSubagentToolName(name string) bool { return toolexecution.IsSubagentToolName(name) }
