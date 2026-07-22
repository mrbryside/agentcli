package agentcli

import (
	"bytes"
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
