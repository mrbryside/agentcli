package agentcli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
)

func TestTerminalLoadingUsesASeparateStatusRow(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	var output bytes.Buffer
	state := &terminalLoadingState{}
	state.attach(renderer, &output)
	terminal := terminal{out: &output, interactive: true, loading: state, stream: renderer}
	loading := terminal.loadingController()

	loading.Start("Thinking")
	firstSource, firstStatus := terminalRendererSnapshot(renderer)
	if firstSource != "" || !strings.Contains(firstStatus, "Thinking") || strings.Contains(firstStatus, "❯") {
		t.Fatalf("initial loading state source=%q status=%q", firstSource, firstStatus)
	}

	deadline := time.Now().Add(time.Second)
	for {
		_, current := terminalRendererSnapshot(renderer)
		if current != firstStatus {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("loading prompt did not animate")
		}
		time.Sleep(terminalLoadingInterval / 3)
	}

	loading.Start("Running read")
	_, current := terminalRendererSnapshot(renderer)
	if !strings.Contains(current, "Running read") {
		t.Fatalf("tool loading status = %q", current)
	}
	loading.Stop()
	_, current = terminalRendererSnapshot(renderer)
	if current != "" {
		t.Fatalf("stopped loading status = %q", current)
	}
}

func TestStaleLoadingControllerCannotClearCurrentView(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	var output bytes.Buffer
	state := &terminalLoadingState{}
	state.attach(renderer, &output)
	terminal := terminal{out: &output, interactive: true, loading: state, stream: renderer}
	oldView := terminal.loadingController()
	currentView := terminal.loadingController()

	oldView.Start("Old view")
	currentView.Start("Current view")
	oldView.Stop()
	_, status := terminalRendererSnapshot(renderer)
	if !strings.Contains(status, "Current view") {
		t.Fatalf("stale controller cleared current status: %q", status)
	}

	terminal.stopLoading()
	_, status = terminalRendererSnapshot(renderer)
	if status != "" {
		t.Fatalf("global stop status = %q", status)
	}
}

func TestTerminalLoadingFollowsAgentEventPhases(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	state := &terminalLoadingState{}
	var output bytes.Buffer
	state.attach(renderer, &output)
	client := terminalClient{
		terminal:           terminal{out: &output, interactive: true, loading: state, stream: renderer},
		pendingPermissions: make(map[permission.ID]permission.Request),
		permissionSubagent: make(map[permission.ID]string),
	}
	loading := client.terminal.loadingController()
	wroteContent := false

	loading.Start("Thinking")
	client.renderEventWithLoading(agentruntime.AgentEvent{
		Type:          agentruntime.ProviderEventReceived,
		ProviderEvent: provider.StreamEvent{Type: provider.ContentReceived, Content: "answer"},
	}, &wroteContent, loading)
	if source, status := terminalRendererSnapshot(renderer); status != "" || source != "answer" || !wroteContent {
		t.Fatalf("content phase source=%q status=%q wrote=%v", source, status, wroteContent)
	}

	client.renderEventWithLoading(agentruntime.AgentEvent{
		Type: agentruntime.ToolCallRequested,
		ToolRequest: &agentruntime.ToolRequest{Call: agentruntime.ToolCall{
			Name: "read",
		}},
	}, &wroteContent, loading)
	if _, status := terminalRendererSnapshot(renderer); !strings.Contains(status, "Running read") {
		t.Fatalf("tool phase status = %q", status)
	}

	client.renderEventWithLoading(agentruntime.AgentEvent{
		Type: agentruntime.ToolResultReceived,
		ToolResult: &agentruntime.ToolResultEnvelope{Result: agentruntime.ToolResult{
			Name: "read", Status: agentruntime.ToolResultSucceeded,
		}},
	}, &wroteContent, loading)
	if _, status := terminalRendererSnapshot(renderer); status != terminalLoadingFrames[0] {
		t.Fatalf("post-tool status = %q", status)
	}

	client.renderEventWithLoading(agentruntime.AgentEvent{
		Type:       agentruntime.AgentPermissionRequested,
		Permission: &permission.Request{ID: "permission_1", ToolName: "write"},
	}, &wroteContent, loading)
	if _, status := terminalRendererSnapshot(renderer); status != "" {
		t.Fatalf("permission status = %q", status)
	}
}

func TestTerminalLoadingTracksConcurrentTasksIndependently(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	state := &terminalLoadingState{}
	var output bytes.Buffer
	state.attach(renderer, &output)
	client := terminalClient{terminal: terminal{
		out: &output, interactive: true, loading: state, stream: renderer,
	}}
	loading := client.terminal.loadingController()
	wroteContent := false

	for _, call := range []agentruntime.ToolCall{
		{CallID: "research", Name: TaskToolName, Arguments: json.RawMessage(`{"agent":"researcher","description":"Inspect queues","prompt":"work"}`)},
		{CallID: "review", Name: TaskToolName, Arguments: json.RawMessage(`{"agent":"reviewer","description":"Review safety","prompt":"work"}`)},
	} {
		client.renderEventWithLoading(agentruntime.AgentEvent{
			Type:        agentruntime.ToolCallRequested,
			ToolRequest: &agentruntime.ToolRequest{Call: call},
		}, &wroteContent, loading)
	}

	_, status := terminalRendererSnapshot(renderer)
	if !strings.Contains(status, "researcher · Inspect queues") || !strings.Contains(status, "reviewer · Review safety") {
		t.Fatalf("concurrent task status = %q", status)
	}
	if strings.Count(status, "\n") != 1 {
		t.Fatalf("concurrent tasks did not render on separate rows: %q", status)
	}

	client.renderEventWithLoading(taskResultEvent(t, "review", "reviewer", TaskStateCompleted), &wroteContent, loading)
	_, status = terminalRendererSnapshot(renderer)
	if !strings.Contains(status, "researcher · Inspect queues") || !strings.Contains(status, "✓ reviewer · completed") {
		t.Fatalf("out-of-order task completion status = %q", status)
	}

	client.renderEventWithLoading(taskResultEvent(t, "research", "researcher", TaskStateCompleted), &wroteContent, loading)
	_, status = terminalRendererSnapshot(renderer)
	if status != terminalLoadingFrames[0] {
		t.Fatalf("post-task loading status = %q", status)
	}
	rendered := terminalANSIEscape.ReplaceAllString(output.String(), "")
	if !strings.Contains(rendered, "✓ researcher · completed") || !strings.Contains(rendered, "✓ reviewer · completed") {
		t.Fatalf("final task rows were not committed: %q", rendered)
	}
}

func TestTerminalLoadingRetainsTaskProgressWhileRootViewIsHidden(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	state := &terminalLoadingState{}
	var output bytes.Buffer
	state.attach(renderer, &output)
	loading := (&terminal{out: &output, interactive: true, loading: state, stream: renderer}).loadingController()
	client := terminalClient{
		terminal:         loading.terminal,
		mainAgentLoading: loading,
	}

	if !loading.StartTask("research", "researcher", "Inspect queues", false) ||
		!loading.StartTask("review", "reviewer", "Review safety", false) {
		t.Fatal("hidden task calls were not tracked")
	}
	client.observeEvent(taskResultEvent(t, "review", "reviewer", TaskStateCompleted))
	loading.Start("")
	_, status := terminalRendererSnapshot(renderer)
	if !strings.Contains(status, "researcher · Inspect queues") || !strings.Contains(status, "✓ reviewer · completed") {
		t.Fatalf("restored root task status = %q", status)
	}

	client.observeEvent(taskResultEvent(t, "research", "researcher", TaskStateCompleted))
	loading.Start("")
	_, status = terminalRendererSnapshot(renderer)
	if status != terminalLoadingFrames[0] {
		t.Fatalf("completed hidden task batch left stale rows: %q", status)
	}
}

func taskResultEvent(t *testing.T, callID, agent string, state TaskState) agentruntime.AgentEvent {
	t.Helper()
	output, err := json.Marshal(TaskResult{
		TaskID: callID, AgentName: agent, State: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	return agentruntime.AgentEvent{
		Type: agentruntime.ToolResultReceived,
		ToolResult: &agentruntime.ToolResultEnvelope{Result: agentruntime.ToolResult{
			CallID: callID, Name: TaskToolName,
			Status: agentruntime.ToolResultSucceeded, Output: output,
		}},
	}
}

func TestTerminalContentStopsLoadingAndWritesImmediately(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	loadingState := &terminalLoadingState{}
	var output bytes.Buffer
	loadingState.attach(renderer, &output)
	terminal := terminal{
		out:         &output,
		interactive: true,
		loading:     loadingState,
		stream:      renderer,
	}

	loading := terminal.loadingController()
	loading.Start("Thinking")
	terminal.write("answer")
	if source, status := terminalRendererSnapshot(renderer); source != "answer" || status != "" {
		t.Fatalf("provider state source=%q status=%q", source, status)
	}

	time.Sleep(terminalLoadingInterval * 2)
	if _, status := terminalRendererSnapshot(renderer); status != "" {
		t.Fatalf("loading animation continued after content: %q", status)
	}
}

func TestTerminalReasoningDoesNotReplaceLoadingIndicator(t *testing.T) {
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	loadingState := &terminalLoadingState{}
	var output bytes.Buffer
	loadingState.attach(renderer, &output)
	terminal := terminal{out: &output, interactive: true, loading: loadingState, stream: renderer}
	client := terminalClient{terminal: terminal}
	loading := terminal.loadingController()
	loading.Start("")
	defer loading.Stop()

	wroteContent := false
	client.renderEventWithLoading(agentruntime.AgentEvent{
		Type: agentruntime.ProviderEventReceived,
		ProviderEvent: provider.StreamEvent{
			Type: provider.ReasoningReceived, Reasoning: "considering options",
		},
	}, &wroteContent, loading)

	renderer.mu.Lock()
	rendered := renderer.renderMarkdownLocked()
	status := renderer.status
	renderer.mu.Unlock()
	if !strings.Contains(terminalANSIEscape.ReplaceAllString(rendered, ""), "> thinking") {
		t.Fatalf("reasoning display = %q", rendered)
	}
	if status != terminalLoadingFrames[0] {
		t.Fatalf("reasoning replaced loading indicator with %q", status)
	}
	if wroteContent {
		t.Fatal("reasoning was treated as assistant content")
	}
}

func terminalRendererSnapshot(renderer *terminalStreamRenderer) (source, status string) {
	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	return renderer.source, renderer.status
}
