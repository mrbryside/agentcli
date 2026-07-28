package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
)

func TestTerminalPermissionAndUnrestrictedRendering(t *testing.T) {
	var output bytes.Buffer
	terminal := terminal{out: &output}
	terminal.permission(permission.Request{ID: "perm_1", ToolName: "guarded_echo", Details: `{"message":"hello"}`})
	terminal.unrestricted()
	text := output.String()
	for _, wanted := range []string{"perm_1", "1. Allow once", "2. Allow for this session", "3. Allow for this project", "4. Deny", "/allow ID", "unrestricted"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("output %q missing %q", text, wanted)
		}
	}
}

func TestTerminalHistoryRendersAssistantMarkdown(t *testing.T) {
	var output bytes.Buffer
	terminal := terminal{out: &output, color: true, interactive: true}

	terminal.messages([]agentruntime.Message{{
		Type:      agentruntime.MessageTypeAssistant,
		Content:   "### What This Demonstrates\n\n- Message storage",
		Reasoning: "inspect the stored message",
	}})

	plain := terminalANSIEscape.ReplaceAllString(output.String(), "")
	if strings.Contains(plain, "### What This Demonstrates") {
		t.Fatalf("assistant history contains raw heading syntax: %q", plain)
	}
	for _, wanted := range []string{"> thinking", "What This Demonstrates", "• Message storage"} {
		if !strings.Contains(plain, wanted) {
			t.Fatalf("assistant history %q missing %q", plain, wanted)
		}
	}
	if strings.Contains(plain, "Agent ·") {
		t.Fatalf("assistant history contains a replay-only role prefix: %q", plain)
	}
}

func TestTerminalHistorySkipsEmptyAssistantMessages(t *testing.T) {
	var output bytes.Buffer
	terminal := terminal{out: &output, interactive: true}

	terminal.messages([]agentruntime.Message{
		{Type: agentruntime.MessageTypeAssistant},
		{Type: agentruntime.MessageTypeAssistant, Content: "visible answer"},
	})

	plain := terminalANSIEscape.ReplaceAllString(output.String(), "")
	if strings.Contains(plain, "Agent ·") || strings.Count(plain, "visible answer") != 1 {
		t.Fatalf("assistant history rendered inconsistently: %q", plain)
	}
}

func TestTerminalHistoryExpandsAllReasoning(t *testing.T) {
	var output bytes.Buffer
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	renderer.configureReasoningExpanded(true)
	terminal := terminal{out: &output, color: true, interactive: true, stream: renderer}

	terminal.messages([]agentruntime.Message{
		{Type: agentruntime.MessageTypeAssistant, Content: "first", Reasoning: "one\ntwo"},
		{Type: agentruntime.MessageTypeAssistant, Content: "second", Reasoning: "three"},
	})

	plain := terminalANSIEscape.ReplaceAllString(output.String(), "")
	if strings.Count(plain, "⌄ thinking") != 2 {
		t.Fatalf("expanded history did not render every reasoning block: %q", plain)
	}
	for _, wanted := range []string{"  one\n  two", "  three"} {
		if !strings.Contains(plain, wanted) {
			t.Fatalf("expanded history %q missing %q", plain, wanted)
		}
	}
}

func TestPermissionChoice(t *testing.T) {
	tests := []struct {
		input string
		want  permission.DecisionType
	}{
		{"1", permission.AllowOnce},
		{"2", permission.AllowSession},
		{"3", permission.AllowProject},
		{"4", permission.Deny},
	}
	for _, test := range tests {
		got, ok := permissionChoice(test.input)
		if !ok || got != test.want {
			t.Fatalf("permissionChoice(%q) = (%q, %v), want (%q, true)", test.input, got, ok, test.want)
		}
	}
	if got, ok := permissionChoice("5"); ok || got != "" {
		t.Fatalf("permissionChoice(5) = (%q, %v), want no choice", got, ok)
	}
}

func TestTerminalListsSkills(t *testing.T) {
	var output bytes.Buffer
	terminal := terminal{out: &output}
	terminal.skills([]Skill{{Name: "testing-go", Description: "Runs Go tests when requested."}})
	if got := output.String(); !strings.Contains(got, "testing-go") || !strings.Contains(got, "Runs Go tests") {
		t.Fatalf("output = %q", got)
	}
}

func TestParsePermissionMode(t *testing.T) {
	tests := map[string]permission.Mode{
		"default":      permission.Default,
		"ACCEPTEDITS":  permission.AcceptEdits,
		"criticalOnly": permission.CriticalOnly,
		"dontask":      permission.DontAsk,
		"plan":         permission.Plan,
		"unrestricted": permission.Unrestricted,
	}
	for input, want := range tests {
		got, ok := parsePermissionMode(input)
		if !ok || got != want {
			t.Fatalf("parsePermissionMode(%q) = (%q, %v), want (%q, true)", input, got, ok, want)
		}
	}
	if got, ok := parsePermissionMode("unknown"); ok || got != "" {
		t.Fatalf("parsePermissionMode(unknown) = (%q, %v), want no mode", got, ok)
	}
}

func TestTerminalCommandChangesAndShowsPermissionMode(t *testing.T) {
	var output bytes.Buffer
	agent := &terminalAgentStub{mode: permission.Default}
	client := terminalClient{agent: agent, terminal: terminal{out: &output}}

	if handled, exit := client.command("/mode criticalOnly"); !handled || exit {
		t.Fatalf("change command = (%v, %v)", handled, exit)
	}
	if agent.mode != permission.CriticalOnly {
		t.Fatalf("mode = %q", agent.mode)
	}
	if handled, exit := client.command("/mode"); !handled || exit {
		t.Fatalf("show command = (%v, %v)", handled, exit)
	}
	for _, wanted := range []string{"default → criticalOnly", "Permission mode · criticalOnly"} {
		if !strings.Contains(output.String(), wanted) {
			t.Fatalf("output %q missing %q", output.String(), wanted)
		}
	}
}

func TestTerminalCommandLetsActiveRunEventRenderModeChange(t *testing.T) {
	var output bytes.Buffer
	agent := &terminalAgentStub{mode: permission.Default}
	client := terminalClient{agent: agent, terminal: terminal{out: &output}, runActive: true}

	if handled, exit := client.command("/mode criticalOnly"); !handled || exit {
		t.Fatalf("change command = (%v, %v)", handled, exit)
	}
	if output.Len() != 0 {
		t.Fatalf("command rendered duplicate transition before run event: %q", output.String())
	}
	wrote := false
	client.renderEvent(agentruntime.AgentEvent{
		Type: agentruntime.PermissionModeChanged,
		PermissionMode: &agentruntime.PermissionModeChange{
			Previous: permission.Default,
			Current:  permission.CriticalOnly,
		},
	}, &wrote)
	if strings.Count(output.String(), "default → criticalOnly") != 1 {
		t.Fatalf("output = %q, want one transition", output.String())
	}
}

func TestTerminalRendersPermissionModeEvents(t *testing.T) {
	var output bytes.Buffer
	client := terminalClient{terminal: terminal{out: &output}}
	wrote := true
	client.renderEvent(agentruntime.AgentEvent{
		Type: agentruntime.PermissionModeChanged,
		PermissionMode: &agentruntime.PermissionModeChange{
			Previous: permission.CriticalOnly,
			Current:  permission.Unrestricted,
		},
	}, &wrote)
	if wrote || !strings.Contains(output.String(), "criticalOnly → unrestricted") {
		t.Fatalf("wrote=%v output=%q", wrote, output.String())
	}
}

func TestTerminalRendersTaskToolResult(t *testing.T) {
	var output bytes.Buffer
	client := terminalClient{terminal: terminal{out: &output}}
	wrote := false
	client.renderEvent(agentruntime.AgentEvent{
		Type: agentruntime.ToolResultReceived,
		ToolResult: &agentruntime.ToolResultEnvelope{Result: agentruntime.ToolResult{
			Name: TaskToolName, Status: agentruntime.ToolResultSucceeded,
			Output: json.RawMessage(`{"task_id":"task_1","agent":"researcher","state":"completed","output":"done"}`),
		}},
	}, &wrote)
	for _, wanted := range []string{"✓ task"} {
		if !strings.Contains(output.String(), wanted) {
			t.Fatalf("output %q missing %q", output.String(), wanted)
		}
	}
}

func TestNextPendingPermissionUsesDisplayOrderAndSkipsResolved(t *testing.T) {
	client := terminalClient{
		pendingPermissions: map[permission.ID]permission.Request{
			"second": {ID: "second"},
		},
		permissionOrder: []permission.ID{"resolved", "second"},
	}
	id, request, ok := client.nextPendingPermission()
	if !ok || id != "second" || request.ID != "second" {
		t.Fatalf("next pending = (%q, %+v, %v)", id, request, ok)
	}
}

func TestTerminalApprovalQueueRendersOneGlobalPromptAtATime(t *testing.T) {
	var output bytes.Buffer
	permissionRequest := permission.Request{ID: "permission", ToolName: "write", Details: "file"}
	confirmationRequest := confirmation.Request{ID: "confirmation", ToolName: "publish", Title: "Publish"}
	client := terminalClient{
		terminal:             terminal{out: &output},
		pendingPermissions:   map[permission.ID]permission.Request{permissionRequest.ID: permissionRequest},
		pendingConfirmations: map[confirmation.ID]confirmation.Request{confirmationRequest.ID: confirmationRequest},
	}
	client.queueApproval(terminalApprovalPermission, string(permissionRequest.ID))
	client.queueApproval(terminalApprovalConfirmation, string(confirmationRequest.ID))
	if got := output.String(); !strings.Contains(got, "permission write") || strings.Contains(got, "Publish") {
		t.Fatalf("first rendered approval = %q, want only permission", got)
	}
	client.completeApproval(terminalApprovalPermission, string(permissionRequest.ID))
	if got := output.String(); !strings.Contains(got, "Publish") {
		t.Fatalf("next rendered approval = %q, want confirmation", got)
	}
}

func TestTerminalApprovalEventsRenderWithoutReentrantLock(t *testing.T) {
	var output bytes.Buffer
	permissionRequest := permission.Request{ID: "permission", ToolName: "edit", Details: "Path: .agentcli/MAIN.md"}
	confirmationRequest := confirmation.Request{ID: "confirmation", ToolName: "edit", Title: "Confirm exact file edit"}
	client := terminalClient{
		terminal:             terminal{out: &output},
		pendingPermissions:   make(map[permission.ID]permission.Request),
		pendingConfirmations: make(map[confirmation.ID]confirmation.Request),
		permissionSubagent:   make(map[permission.ID]string),
		confirmationSubagent: make(map[confirmation.ID]string),
	}

	renderTerminalEventWithoutDeadlock(t, &client, agentruntime.AgentEvent{
		Type:       agentruntime.AgentPermissionRequested,
		Permission: &permissionRequest,
	})
	if got := output.String(); !strings.Contains(got, "permission edit") {
		t.Fatalf("permission event output = %q", got)
	}

	renderTerminalEventWithoutDeadlock(t, &client, agentruntime.AgentEvent{
		Type:         agentruntime.AgentConfirmationRequested,
		Confirmation: &confirmationRequest,
	})
	if got := output.String(); strings.Contains(got, confirmationRequest.Title) {
		t.Fatalf("confirmation rendered before permission resolved: %q", got)
	}

	renderTerminalEventWithoutDeadlock(t, &client, agentruntime.AgentEvent{
		Type:       agentruntime.AgentPermissionResolved,
		Permission: &permissionRequest,
	})
	if got := output.String(); !strings.Contains(got, confirmationRequest.Title) {
		t.Fatalf("queued confirmation was not rendered after permission resolution: %q", got)
	}
}

func renderTerminalEventWithoutDeadlock(t *testing.T, client *terminalClient, event agentruntime.AgentEvent) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wroteContent := false
		client.renderInView("", func() {
			client.renderEvent(event, &wroteContent)
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal event rendering deadlocked")
	}
}

func TestTerminalApprovalQueueDoesNotResolveAnotherApprovalKind(t *testing.T) {
	var output bytes.Buffer
	confirmationRequest := confirmation.Request{ID: "confirmation", ToolName: "publish", Title: "Publish"}
	permissionRequest := permission.Request{ID: "permission", ToolName: "write"}
	client := terminalClient{
		terminal:             terminal{out: &output},
		pendingConfirmations: map[confirmation.ID]confirmation.Request{confirmationRequest.ID: confirmationRequest},
		pendingPermissions:   map[permission.ID]permission.Request{permissionRequest.ID: permissionRequest},
		permissionOrder:      []permission.ID{permissionRequest.ID},
	}
	client.queueApproval(terminalApprovalConfirmation, string(confirmationRequest.ID))
	client.queueApproval(terminalApprovalPermission, string(permissionRequest.ID))

	if handled, exit := client.command("1"); !handled || exit {
		t.Fatalf("mismatched approval answer = (%v, %v), want handled without exit", handled, exit)
	}
	active, found := client.activeApprovalSnapshot()
	if !found || active.kind != terminalApprovalConfirmation || active.id != string(confirmationRequest.ID) {
		t.Fatalf("active approval = (%#v, %v), want original confirmation", active, found)
	}
	if _, found := client.pendingPermission(permissionRequest.ID); !found {
		t.Fatal("queued permission was incorrectly resolved")
	}
	if !strings.Contains(output.String(), "answer the active confirmation with yes or no") {
		t.Fatalf("output = %q, want active-confirmation guidance", output.String())
	}
}

func TestTerminalConfirmationUsesYesNoAndRoutesOwnedSubagent(t *testing.T) {
	var output bytes.Buffer
	agent := &terminalAgentStub{}
	mainAgentRequest := confirmation.Request{ID: "confirm_mainAgent", SessionID: "mainAgent", TurnID: "turn-mainAgent", CallID: "call-mainAgent", ToolName: "publish", Title: "Publish report", Message: "Publish now?", Details: "Destination: production"}
	client := terminalClient{
		agent: agent, terminal: terminal{out: &output}, sessionID: "mainAgent",
		pendingConfirmations: map[confirmation.ID]confirmation.Request{mainAgentRequest.ID: mainAgentRequest},
		confirmationOrder:    []confirmation.ID{mainAgentRequest.ID}, confirmationSubagent: make(map[confirmation.ID]string),
	}
	client.terminal.confirmation(mainAgentRequest)
	if handled, exit := client.command("yes"); !handled || exit {
		t.Fatalf("yes command = (%v, %v)", handled, exit)
	}
	if agent.confirmationDecision.ConfirmationID != mainAgentRequest.ID || agent.confirmationDecision.Answer != confirmation.Yes {
		t.Fatalf("mainAgent decision = %#v", agent.confirmationDecision)
	}

	subagentRequest := confirmation.Request{ID: "confirm_subagent", SessionID: "subagent", TurnID: "turn-subagent", CallID: "call-subagent", ToolName: "delete", Message: "Continue?"}
	client.pendingConfirmations[subagentRequest.ID] = subagentRequest
	client.confirmationOrder = append(client.confirmationOrder, subagentRequest.ID)
	client.confirmationSubagent[subagentRequest.ID] = "subagent_1"
	if handled, exit := client.command("/decline confirm_subagent"); !handled || exit {
		t.Fatalf("decline command = (%v, %v)", handled, exit)
	}
	if agent.confirmationSubagentID != "subagent_1" || agent.confirmationDecision.Answer != confirmation.No {
		t.Fatalf("subagent decision = %#v subagent=%q", agent.confirmationDecision, agent.confirmationSubagentID)
	}
	for _, expected := range []string{"Publish report", "Destination: production", "Publish now?", "Yes", "No", "Type y/n"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("confirmation output %q missing %q", output.String(), expected)
		}
	}
}

func TestTerminalSubagentCommandsNavigateWithoutReadingMainAgentObservation(t *testing.T) {
	var output bytes.Buffer
	agent := &terminalAgentStub{
		mode:        permission.Default,
		definitions: []SubagentDefinition{{Name: "researcher", Description: "Researches options.", Provider: "openai", Model: "small"}},
		subagents: []storage.Subagent{{
			ID: "subagent_1", DisplayName: "Mira", MainAgentSessionID: "mainAgent", SubagentSessionID: "subagent", DefinitionName: "researcher", Status: storage.SubagentStatusRunning,
		}},
		messages: map[string][]agentruntime.Message{"subagent": {
			{Type: agentruntime.MessageTypeUser, Content: "Compare queues."},
			{Type: agentruntime.MessageTypeAssistant, Content: "Here is the comparison."},
		}},
	}
	client := terminalClient{agent: agent, terminal: terminal{out: &output}, sessionID: "mainAgent"}

	if handled, exit := client.command("/agents"); !handled || exit {
		t.Fatalf("/agents = (%v, %v)", handled, exit)
	}
	if handled, exit := client.command("/agent-status mira"); !handled || exit {
		t.Fatalf("/agent-status = (%v, %v)", handled, exit)
	}
	if handled, exit := client.command("/agent MIRA"); !handled || exit {
		t.Fatalf("/agent = (%v, %v)", handled, exit)
	}
	if client.subagentID != "subagent_1" {
		t.Fatalf("active subagent = %q", client.subagentID)
	}
	if err := client.runSubagentTurn(context.Background(), "Add recovery notes.", nil); err != nil {
		t.Fatalf("follow-up error = %v", err)
	}
	if agent.sentMessage != "Add recovery notes." {
		t.Fatalf("follow-up = %q", agent.sentMessage)
	}
	if handled, exit := client.command("/close Mira"); !handled || exit || client.subagentID != "" {
		t.Fatalf("/close = (%v, %v), active = %q", handled, exit, client.subagentID)
	}
	if handled, exit := client.command("/back"); !handled || exit || client.subagentID != "" {
		t.Fatalf("/back = (%v, %v), active = %q", handled, exit, client.subagentID)
	}
	if agent.readSubagentCalls != 0 {
		t.Fatalf("terminal used ReadSubagent %d times", agent.readSubagentCalls)
	}
	for _, wanted := range []string{"researcher", "Mira", "skills=none", "tools=none", "subagent_1", "Subagent status · subagent_1 · running · Working on: researcher", "Compare queues.", "Here is the comparison.", "Closed subagent · subagent_1", "Session · mainAgent"} {
		if !strings.Contains(output.String(), wanted) {
			t.Fatalf("output %q missing %q", output.String(), wanted)
		}
	}
}

func TestTerminalSubagentCommandsRejectUnknownAndClosedInstances(t *testing.T) {
	var output bytes.Buffer
	agent := &terminalAgentStub{subagents: []storage.Subagent{{ID: "closed", MainAgentSessionID: "mainAgent", SubagentSessionID: "subagent", Status: storage.SubagentStatusClosed}}}
	client := terminalClient{agent: agent, terminal: terminal{out: &output}, sessionID: "mainAgent"}
	client.command("/agent missing")
	client.command("/agent closed")
	client.command("/close missing")
	for _, wanted := range []string{"subagent missing was not found in this session", "subagent closed is closed"} {
		if !strings.Contains(output.String(), wanted) {
			t.Fatalf("output %q missing %q", output.String(), wanted)
		}
	}
}

func TestTerminalBackDetachesSubagentRendererWithoutStoppingSubagent(t *testing.T) {
	var output bytes.Buffer
	agent := &terminalAgentStub{
		subagents: []storage.Subagent{{
			ID: "subagent_1", MainAgentSessionID: "mainAgent", SubagentSessionID: "subagent", DefinitionName: "researcher",
			Status: storage.SubagentStatusRunning, CurrentSubagentTurnID: "subagent-turn",
		}},
		messages: map[string][]agentruntime.Message{
			"mainAgent": {{Type: agentruntime.MessageTypeAssistant, Content: "mainAgent-only"}},
			"subagent":  {{Type: agentruntime.MessageTypeAssistant, Content: "subagent-only"}},
		},
	}
	client := terminalClient{agent: agent, terminal: terminal{out: &output, color: true, interactive: true}, modelName: "test", sessionID: "mainAgent"}
	client.switchView("")
	if err := client.openSubagent("subagent_1"); err != nil {
		t.Fatal(err)
	}
	subagentContext, ok := client.activeViewContext("subagent_1")
	if !ok {
		t.Fatal("subagent view context is unavailable")
	}
	if handled, exit := client.command("/back"); !handled || exit {
		t.Fatalf("/back = (%v, %v)", handled, exit)
	}
	select {
	case <-subagentContext.Done():
	default:
		t.Fatal("/back did not detach the subagent renderer")
	}
	if client.activeView() != "" {
		t.Fatalf("active view = %q, want mainAgent", client.activeView())
	}
	if agent.subagents[0].Status != storage.SubagentStatusRunning {
		t.Fatalf("subagent status = %q, want running", agent.subagents[0].Status)
	}
	lastClear := strings.LastIndex(output.String(), "\x1b[2J\x1b[H")
	visibleMainAgent := output.String()
	if lastClear >= 0 {
		visibleMainAgent = visibleMainAgent[lastClear:]
	}
	if !strings.Contains(visibleMainAgent, "mainAgent-only") || strings.Contains(visibleMainAgent, "subagent-only") {
		t.Fatalf("mainAgent view was not isolated: %q", visibleMainAgent)
	}
	if err := client.openSubagent("subagent_1"); err != nil {
		t.Fatal(err)
	}
	resumedContext, ok := client.activeViewContext("subagent_1")
	if !ok || resumedContext == subagentContext {
		t.Fatal("reopening a streaming subagent did not attach a new renderer")
	}
	select {
	case <-resumedContext.Done():
		t.Fatal("resumed subagent renderer was already detached")
	default:
	}
}

func TestTerminalSessionReportsSelectedViewStreamingState(t *testing.T) {
	var output bytes.Buffer
	agent := &terminalAgentStub{subagents: []storage.Subagent{{
		ID: "subagent_1", MainAgentSessionID: "mainAgent", SubagentSessionID: "subagent",
		Status: storage.SubagentStatusRunning, CurrentSubagentTurnID: "subagent-turn",
	}}}
	client := terminalClient{agent: agent, terminal: terminal{out: &output}, sessionID: "mainAgent"}
	client.switchView("subagent_1")

	if handled, exit := client.command("/session"); !handled || exit {
		t.Fatalf("/session = (%v, %v)", handled, exit)
	}
	if got := output.String(); !strings.Contains(got, "Streaming · active") {
		t.Fatalf("output = %q, want active stream status", got)
	}

	output.Reset()
	client.switchView("")
	client.command("/session")
	if got := output.String(); !strings.Contains(got, "Streaming · idle") {
		t.Fatalf("output = %q, want idle stream status", got)
	}
}

func TestTerminalMainAgentPromptQueueIsFIFOAndCanBeCleared(t *testing.T) {
	client := terminalClient{}
	if position := client.enqueueMainAgentPrompt(" \t\n "); position != 0 {
		t.Fatalf("blank queue position = %d, want 0", position)
	}
	if _, ok := client.dequeueMainAgentPrompt(); ok {
		t.Fatal("blank input was queued")
	}
	if position := client.enqueueMainAgentPrompt("second"); position != 1 {
		t.Fatalf("first queue position = %d, want 1", position)
	}
	if position := client.enqueueMainAgentPrompt("third"); position != 2 {
		t.Fatalf("second queue position = %d, want 2", position)
	}
	for _, want := range []string{"second", "third"} {
		got, ok := client.dequeueMainAgentPrompt()
		if !ok || got != want {
			t.Fatalf("dequeue = (%q, %v), want (%q, true)", got, ok, want)
		}
	}
	if _, ok := client.dequeueMainAgentPrompt(); ok {
		t.Fatal("empty queue returned a prompt")
	}

	client.enqueueMainAgentPrompt("discard on new session")
	client.clearMainAgentPrompts()
	if _, ok := client.dequeueMainAgentPrompt(); ok {
		t.Fatal("cleared queue returned a prompt")
	}
}

func TestAgentRunTerminalUsesSelectedSessionAndLeavesAgentOpen(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	var output bytes.Buffer
	if err := agent.RunTerminal(
		WithTerminalInput(strings.NewReader("/exit\n")),
		WithTerminalOutput(&output),
		WithTerminalSessionID("playground-session"),
	); err != nil {
		t.Fatalf("RunTerminal() error = %v", err)
	}
	if !strings.Contains(output.String(), "playground-session") || !strings.Contains(output.String(), "Goodbye.") {
		t.Fatalf("terminal output = %q", output.String())
	}

	run, subscription, err := agent.StartSubscribed(context.Background(), agentruntime.Request{
		SessionID: "after-playground",
		Message:   agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "continue"},
	})
	if err != nil {
		t.Fatalf("agent was not reusable after terminal exit: %v", err)
	}
	for range subscription.Events {
	}
	if _, err := run.Result(); err != nil {
		t.Fatalf("post-terminal run failed: %v", err)
	}
}

func TestTerminalModelLabelIncludesContextWindow(t *testing.T) {
	model := &terminalMetadataModel{
		scriptedModel: &scriptedModel{},
		metadata:      agentruntime.ModelMetadata{ContextWindowTokens: 122_880, MaxOutputTokens: 66_560},
	}
	if got, want := terminalModelLabel("qwen3.6-35b", model), "qwen3.6-35b · 120k context"; got != want {
		t.Fatalf("terminalModelLabel() = %q, want %q", got, want)
	}
}

func TestFormatTerminalTokenCountUsesBinaryUnits(t *testing.T) {
	for tokens, want := range map[int]string{
		122_880: "120k",
		66_560:  "65k",
	} {
		if got := formatTerminalTokenCount(tokens); got != want {
			t.Fatalf("formatTerminalTokenCount(%d) = %q, want %q", tokens, got, want)
		}
	}
}

func TestTerminalModelLabelShowsDashWithoutMetadata(t *testing.T) {
	if got, want := terminalModelLabel("custom", &scriptedModel{}), "custom · - context"; got != want {
		t.Fatalf("terminalModelLabel() = %q, want %q", got, want)
	}
}

type terminalMetadataModel struct {
	*scriptedModel
	metadata agentruntime.ModelMetadata
}

func (model *terminalMetadataModel) ModelMetadata() (agentruntime.ModelMetadata, error) {
	return model.metadata, nil
}

func TestAgentRunTerminalRejectsInvalidOptions(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if err := agent.RunTerminal(nil); err == nil {
		t.Fatal("nil terminal option was accepted")
	}
	if err := agent.RunTerminal(WithTerminalInput(nil)); err == nil {
		t.Fatal("nil terminal input was accepted")
	}
	if err := agent.RunTerminal(WithTerminalOutput(nil)); err == nil {
		t.Fatal("nil terminal output was accepted")
	}
	if err := agent.RunTerminal(WithTerminalSessionID(" \t ")); err == nil {
		t.Fatal("blank terminal session ID was accepted")
	}
}

func TestClosedAgentRejectsTerminal(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.RunTerminal(
		WithTerminalInput(strings.NewReader("/exit\n")),
		WithTerminalOutput(&bytes.Buffer{}),
	); !errors.Is(err, ErrClosed) {
		t.Fatalf("RunTerminal() error = %v, want ErrClosed", err)
	}
}

func TestTerminalLeavesTaskDeliveryToAgent(t *testing.T) {
	interfaceType := reflect.TypeOf((*terminalAgent)(nil)).Elem()
	for _, name := range []string{"SubscribeSubagentResults", "TryInjectSubagentResult", "ContinueSubagentResultSubscribed"} {
		if _, found := interfaceType.MethodByName(name); found {
			t.Fatalf("terminalAgent still owns %s; task delivery belongs to Agent", name)
		}
	}
}

func TestTerminalInputFallsBackForNonInteractiveReaders(t *testing.T) {
	output := terminal{out: &bytes.Buffer{}, interactive: true}
	inputSession, err := terminalInput(strings.NewReader("hello\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	defer inputSession.close()
	if inputSession.promptManaged {
		t.Fatal("non-terminal input unexpectedly enabled the interactive line editor")
	}
	if inputSession.escapes != nil {
		t.Fatal("non-terminal input unexpectedly enabled escape-key handling")
	}
	if inputSession.reasoningToggles != nil {
		t.Fatal("non-terminal input unexpectedly enabled reasoning toggles")
	}
	if got := <-inputSession.lines; got != "hello" {
		t.Fatalf("line = %q, want hello", got)
	}
	if err := <-inputSession.errors; err != nil {
		t.Fatalf("read error = %v", err)
	}
}

func TestTerminalCtrlCRequiresTwoPressesAndCanBeDisarmed(t *testing.T) {
	var output bytes.Buffer
	client := terminalClient{terminal: terminal{out: &output}}

	if client.handleExitInterrupt() {
		t.Fatal("first Ctrl+C requested exit")
	}
	if !strings.Contains(output.String(), "press Ctrl+C again within 2 seconds to quit") {
		t.Fatalf("first Ctrl+C output = %q", output.String())
	}
	if !client.handleExitInterrupt() {
		t.Fatal("second Ctrl+C did not request exit")
	}
	if !strings.Contains(output.String(), "Goodbye.") {
		t.Fatalf("second Ctrl+C output = %q", output.String())
	}

	client.handleExitInterrupt()
	client.disarmExitInterrupt()
	if client.handleExitInterrupt() {
		t.Fatal("Ctrl+C remained armed after ordinary input")
	}
}

func TestTerminalRendersProviderFragmentsExactlyOnce(t *testing.T) {
	var output bytes.Buffer
	client := terminalClient{terminal: terminal{out: &output, interactive: true}}
	wroteContent := false
	for _, fragment := range []string{"I'm", " your primary", " agent."} {
		client.renderEvent(agentruntime.AgentEvent{
			Type: agentruntime.ProviderEventReceived,
			ProviderEvent: provider.StreamEvent{
				Type:    provider.ContentReceived,
				Content: fragment,
			},
		}, &wroteContent)
	}

	if got, want := output.String(), "I'm your primary agent."; got != want {
		t.Fatalf("rendered provider fragments = %q, want %q", got, want)
	}
	if got := strings.Count(output.String(), "I'm"); got != 1 {
		t.Fatalf("first fragment rendered %d times, want exactly once", got)
	}
}

func TestTerminalSubagentUsesSharedMarkdownAndReasoningRenderer(t *testing.T) {
	var output bytes.Buffer
	renderer := &terminalStreamRenderer{}
	renderer.attach(func() int { return 80 })
	client := terminalClient{terminal: terminal{out: &output, interactive: true, stream: renderer}}
	wroteContent := false

	client.renderSubagentEvent("subagent_1", agentruntime.AgentEvent{
		Type: agentruntime.ProviderEventReceived,
		ProviderEvent: provider.StreamEvent{
			Type:      provider.ReasoningReceived,
			Reasoning: "inspect the subagent task",
		},
	}, &wroteContent)
	client.renderSubagentEvent("subagent_1", agentruntime.AgentEvent{
		Type: agentruntime.ProviderEventReceived,
		ProviderEvent: provider.StreamEvent{
			Type:    provider.ContentReceived,
			Content: "### Subagent result\n\n- complete",
		},
	}, &wroteContent)

	renderer.mu.Lock()
	rendered := renderer.renderMarkdownLocked()
	reasoning := renderer.reasoning
	source := renderer.source
	renderer.mu.Unlock()
	plain := terminalANSIEscape.ReplaceAllString(rendered, "")
	if reasoning != "inspect the subagent task" || source != "### Subagent result\n\n- complete" {
		t.Fatalf("subagent renderer state = reasoning %q source %q", reasoning, source)
	}
	if strings.Contains(plain, "### Subagent result") || !strings.Contains(plain, "> thinking") || !strings.Contains(plain, "Subagent result") || !strings.Contains(plain, "• complete") {
		t.Fatalf("subagent rendered output = %q", plain)
	}
}

func TestTerminalOpenSubagentShowsToolHistoryAndLastTurnFailure(t *testing.T) {
	var output bytes.Buffer
	agent := &terminalAgentStub{
		subagents: []storage.Subagent{{
			ID: "subagent_1", MainAgentSessionID: "mainAgent", SubagentSessionID: "subagent", DefinitionName: "researcher",
			Status: "", LastSubagentTurnID: "turn_1", LastResultError: "maximum provider steps reached",
		}},
		messages: map[string][]agentruntime.Message{"subagent": {
			{Type: agentruntime.MessageTypeUser, Content: "Inspect the project."},
			{Type: agentruntime.MessageTypeToolCall, ToolCalls: []agentruntime.ToolCall{{CallID: "call_1", Name: "load_skill", Arguments: json.RawMessage(`{"name":"interview"}`)}}},
			{Type: agentruntime.MessageTypeToolResult, ToolResult: &agentruntime.ToolResult{CallID: "call_1", Name: "load_skill", Status: agentruntime.ToolResultSucceeded, Output: json.RawMessage(`{"status":"loaded","instructions_in_context":true}`)}},
		}},
	}
	client := terminalClient{agent: agent, terminal: terminal{out: &output}, sessionID: "mainAgent"}

	if err := client.openSubagent("subagent_1"); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"Inspect the project.", "● load_skill", "✓ load_skill", "subagent turn turn_1 failed", "maximum provider steps reached"} {
		if !strings.Contains(output.String(), wanted) {
			t.Fatalf("output %q missing %q", output.String(), wanted)
		}
	}
}

func TestTerminalCloseActiveSubagentReturnsToMainAgent(t *testing.T) {
	var output bytes.Buffer
	agent := &terminalAgentStub{subagents: []storage.Subagent{{ID: "subagent_1", MainAgentSessionID: "mainAgent", SubagentSessionID: "subagent", Status: ""}}}
	client := terminalClient{agent: agent, terminal: terminal{out: &output}, sessionID: "mainAgent", subagentID: "subagent_1"}
	if handled, exit := client.command("/close subagent_1"); !handled || exit {
		t.Fatalf("/close = (%v, %v)", handled, exit)
	}
	if client.subagentID != "" || agent.closedID != "subagent_1" {
		t.Fatalf("active=%q closed=%q", client.subagentID, agent.closedID)
	}
}

func TestTerminalSubagentInputUsesSubagentMailbox(t *testing.T) {
	agent := &terminalAgentStub{subagents: []storage.Subagent{{
		ID: "subagent_1", MainAgentSessionID: "mainAgent", SubagentSessionID: "subagent", Status: storage.SubagentStatusRunning, CurrentSubagentTurnID: "turn_1",
	}}}
	client := terminalClient{agent: agent, terminal: terminal{out: &bytes.Buffer{}}, sessionID: "mainAgent", subagentID: "subagent_1"}
	if err := client.runSubagentTurn(context.Background(), "Please add recovery notes.", nil); err != nil {
		t.Fatalf("runSubagentTurn() error = %v", err)
	}
	if agent.sentMainAgentID != "mainAgent" || agent.sentSubagentID != "subagent_1" || agent.sentMessage != "Please add recovery notes." {
		t.Fatalf("sent = (%q, %q, %q)", agent.sentMainAgentID, agent.sentSubagentID, agent.sentMessage)
	}
}

type terminalAgentStub struct {
	mode                   permission.Mode
	definitions            []SubagentDefinition
	subagents              []storage.Subagent
	messages               map[string][]agentruntime.Message
	closedID               string
	confirmationDecision   confirmation.Decision
	confirmationSubagentID string
	readSubagentCalls      int
	sentMainAgentID        string
	sentSubagentID         string
	sentMessage            string
}

func (*terminalAgentStub) StartSubscribed(context.Context, agentruntime.Request) (*agentruntime.Run, agentruntime.EventSubscription, error) {
	return nil, agentruntime.EventSubscription{}, nil
}

func (*terminalAgentStub) SubscribeSubagentPermissions(context.Context) <-chan SubagentPermissionEvent {
	return make(chan SubagentPermissionEvent)
}

func (*terminalAgentStub) PendingSubagentPermissions(context.Context, string) ([]SubagentPermissionEvent, error) {
	return nil, nil
}

func (*terminalAgentStub) SubscribeSubagentConfirmations(context.Context) <-chan SubagentConfirmationEvent {
	return make(chan SubagentConfirmationEvent)
}

func (*terminalAgentStub) PendingSubagentConfirmations(context.Context, string) ([]SubagentConfirmationEvent, error) {
	return nil, nil
}

func (*terminalAgentStub) ResolvePermission(context.Context, permission.Decision) error {
	return nil
}

func (agent *terminalAgentStub) ResolveConfirmation(_ context.Context, decision confirmation.Decision) error {
	agent.confirmationDecision = decision
	return nil
}

func (*terminalAgentStub) ResolveSubagentPermission(context.Context, string, string, permission.Decision) error {
	return nil
}

func (agent *terminalAgentStub) ResolveSubagentConfirmation(_ context.Context, _ string, subagentID string, decision confirmation.Decision) error {
	agent.confirmationSubagentID = subagentID
	agent.confirmationDecision = decision
	return nil
}

func (agent *terminalAgentStub) SetPermissionMode(_ context.Context, mode permission.Mode) error {
	agent.mode = mode
	return nil
}

func (agent *terminalAgentStub) PermissionMode() permission.Mode {
	return agent.mode
}

func (agent *terminalAgentStub) SubagentDefinitions() []SubagentDefinition {
	return append([]SubagentDefinition(nil), agent.definitions...)
}

func (agent *terminalAgentStub) ListSubagents(_ context.Context, mainAgentSessionID string, _ bool) ([]storage.Subagent, error) {
	instances := make([]storage.Subagent, 0, len(agent.subagents))
	for _, instance := range agent.subagents {
		if instance.MainAgentSessionID == mainAgentSessionID {
			instances = append(instances, instance)
		}
	}
	return instances, nil
}

func (agent *terminalAgentStub) ListMessages(_ context.Context, sessionID string) ([]agentruntime.Message, error) {
	return append([]agentruntime.Message(nil), agent.messages[sessionID]...), nil
}

func (agent *terminalAgentStub) SendSubagentMessage(_ context.Context, mainAgentSessionID, subagentID, message string) (storage.Subagent, error) {
	agent.sentMainAgentID = mainAgentSessionID
	agent.sentSubagentID = subagentID
	agent.sentMessage = message
	return storage.Subagent{ID: subagentID, Status: storage.SubagentStatusRunning}, nil
}

func (agent *terminalAgentStub) CloseSubagent(_ context.Context, _ string, id string) (storage.Subagent, error) {
	agent.closedID = id
	return storage.Subagent{ID: id, Status: storage.SubagentStatusClosed}, nil
}

func (*terminalAgentStub) SubagentRun(context.Context, string, string, string) (*agentruntime.Run, error) {
	return nil, agentruntime.ErrRunNotFound
}
