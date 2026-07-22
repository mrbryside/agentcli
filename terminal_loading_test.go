package agentcli

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
)

type recordingPromptEditor struct {
	mu      sync.Mutex
	prompt  string
	refresh int
	history []string
}

func (editor *recordingPromptEditor) SetPrompt(prompt string) {
	editor.mu.Lock()
	editor.prompt = prompt
	editor.history = append(editor.history, prompt)
	editor.mu.Unlock()
}

func (editor *recordingPromptEditor) Refresh() {
	editor.mu.Lock()
	editor.refresh++
	editor.mu.Unlock()
}

func (editor *recordingPromptEditor) snapshot() (string, int, []string) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.prompt, editor.refresh, append([]string(nil), editor.history...)
}

func TestTerminalLoadingAnimatesPromptAndRestoresInput(t *testing.T) {
	editor := &recordingPromptEditor{}
	state := &terminalLoadingState{}
	state.attach(editor, "❯ ")
	terminal := terminal{interactive: true, loading: state}
	loading := terminal.loadingController()

	loading.Start("Thinking")
	first, refreshes, _ := editor.snapshot()
	if !strings.Contains(first, "Thinking") || !strings.HasSuffix(first, "❯ ") || refreshes == 0 {
		t.Fatalf("initial loading prompt = %q refreshes=%d", first, refreshes)
	}

	deadline := time.Now().Add(time.Second)
	for {
		current, _, _ := editor.snapshot()
		if current != first {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("loading prompt did not animate")
		}
		time.Sleep(terminalLoadingInterval / 3)
	}

	loading.Start("Running read")
	current, _, _ := editor.snapshot()
	if !strings.Contains(current, "Running read") {
		t.Fatalf("tool loading prompt = %q", current)
	}
	loading.Stop()
	current, _, _ = editor.snapshot()
	if current != "❯ " {
		t.Fatalf("restored prompt = %q", current)
	}
}

func TestStaleLoadingControllerCannotClearCurrentView(t *testing.T) {
	editor := &recordingPromptEditor{}
	state := &terminalLoadingState{}
	state.attach(editor, "❯ ")
	terminal := terminal{interactive: true, loading: state}
	oldView := terminal.loadingController()
	currentView := terminal.loadingController()

	oldView.Start("Old view")
	currentView.Start("Current view")
	oldView.Stop()
	prompt, _, _ := editor.snapshot()
	if !strings.Contains(prompt, "Current view") {
		t.Fatalf("stale controller cleared current prompt: %q", prompt)
	}

	terminal.stopLoading()
	prompt, _, _ = editor.snapshot()
	if prompt != "❯ " {
		t.Fatalf("global stop prompt = %q", prompt)
	}
}

func TestTerminalLoadingFollowsAgentEventPhases(t *testing.T) {
	editor := &recordingPromptEditor{}
	state := &terminalLoadingState{}
	state.attach(editor, "❯ ")
	var output bytes.Buffer
	client := terminalClient{
		terminal:           terminal{out: &output, interactive: true, loading: state},
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
	if prompt, _, _ := editor.snapshot(); prompt != "❯ " || !wroteContent || output.String() != "answer" {
		t.Fatalf("content phase prompt=%q wrote=%v output=%q", prompt, wroteContent, output.String())
	}

	client.renderEventWithLoading(agentruntime.AgentEvent{
		Type: agentruntime.ToolCallRequested,
		ToolRequest: &agentruntime.ToolRequest{Call: agentruntime.ToolCall{
			Name: "read",
		}},
	}, &wroteContent, loading)
	if prompt, _, _ := editor.snapshot(); !strings.Contains(prompt, "Running read") {
		t.Fatalf("tool phase prompt = %q", prompt)
	}

	client.renderEventWithLoading(agentruntime.AgentEvent{
		Type: agentruntime.ToolResultReceived,
		ToolResult: &agentruntime.ToolResultEnvelope{Result: agentruntime.ToolResult{
			Name: "read", Status: agentruntime.ToolResultSucceeded,
		}},
	}, &wroteContent, loading)
	if prompt, _, _ := editor.snapshot(); !strings.Contains(prompt, "Thinking") {
		t.Fatalf("post-tool prompt = %q", prompt)
	}

	client.renderEventWithLoading(agentruntime.AgentEvent{
		Type:       agentruntime.AgentPermissionRequested,
		Permission: &permission.Request{ID: "permission_1", ToolName: "write"},
	}, &wroteContent, loading)
	if prompt, _, _ := editor.snapshot(); prompt != "❯ " {
		t.Fatalf("permission prompt = %q", prompt)
	}
}

func TestTerminalStreamOwnsPromptAfterFirstContent(t *testing.T) {
	editor := &recordingPromptEditor{}
	loadingState := &terminalLoadingState{}
	loadingState.attach(editor, "❯ ")
	stream := &terminalStreamOutput{}
	stream.attach(editor, "❯ ")
	var output bytes.Buffer
	terminal := terminal{
		out:         &output,
		interactive: true,
		loading:     loadingState,
		stream:      stream,
	}

	loading := terminal.loadingController()
	loading.Start("Thinking")
	terminal.write("answer")
	waitForPrompt(t, editor, "answer\n❯ ")

	time.Sleep(terminalLoadingInterval * 2)
	if prompt, _, _ := editor.snapshot(); prompt != "answer\n❯ " {
		t.Fatalf("loading animation replaced stream prompt: %q", prompt)
	}
	terminal.println("")
}
