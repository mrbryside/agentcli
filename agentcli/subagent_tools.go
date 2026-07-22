package agentcli

import "harness-api/toolexecution"

// Compatibility aliases keep existing agentcli callers source-compatible;
// all subagent tool definitions and handlers are owned by toolexecution.
const (
	StartSubagentToolName       = toolexecution.StartSubagentToolName
	ListSubagentsToolName       = toolexecution.ListSubagentsToolName
	SubagentStatusToolName      = toolexecution.SubagentStatusToolName
	SendSubagentMessageToolName = toolexecution.SendSubagentMessageToolName
	CloseSubagentToolName       = toolexecution.CloseSubagentToolName
)

var subagentToolNames = map[string]struct{}{
	StartSubagentToolName: {}, ListSubagentsToolName: {}, SubagentStatusToolName: {},
	SendSubagentMessageToolName: {}, CloseSubagentToolName: {},
}

func isSubagentToolName(name string) bool { return toolexecution.IsSubagentToolName(name) }
