package agentcli

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type promptRuntimeContext struct {
	Agent    string
	Provider string
	Model    string
}

func renderPromptRuntimeContext(project *Project, context promptRuntimeContext) string {
	var prompt strings.Builder
	prompt.WriteString("<runtime_context>\n")
	fmt.Fprintf(&prompt, "agent: %s\n", strconv.Quote(strings.TrimSpace(context.Agent)))
	fmt.Fprintf(&prompt, "provider: %s\n", strconv.Quote(strings.TrimSpace(context.Provider)))
	fmt.Fprintf(&prompt, "model: %s\n", strconv.Quote(strings.TrimSpace(context.Model)))
	if project != nil {
		fmt.Fprintf(&prompt, "working_directory: %s\n", strconv.Quote(project.root))
		fmt.Fprintf(&prompt, "workspace_root: %s\n", strconv.Quote(project.root))
		fmt.Fprintf(&prompt, "permission_mode: %s\n", strconv.Quote(string(project.PermissionMode())))
	}
	fmt.Fprintf(&prompt, "platform: %s\n", strconv.Quote(runtime.GOOS+"/"+runtime.GOARCH))
	fmt.Fprintf(&prompt, "date: %s\n", strconv.Quote(time.Now().Format("2006-01-02")))
	prompt.WriteString("</runtime_context>")
	return prompt.String()
}
