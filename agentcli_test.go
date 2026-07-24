package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestNewValidatesRequiredAndNumericOptions(t *testing.T) {
	for _, test := range []struct {
		name    string
		options []Option
	}{
		{name: "missing model"},
		{name: "zero channel buffer", options: []Option{WithModel(&scriptedModel{}), WithChannelBuffer(0)}},
		{name: "zero workers", options: []Option{WithModel(&scriptedModel{}), WithToolWorkers(0)}},
		{name: "empty project root", options: []Option{WithModel(&scriptedModel{}), WithProjectRoot("")}},
		{name: "unknown permission mode", options: []Option{WithModel(&scriptedModel{}), WithPermissionMode("unknown")}},
		{name: "unknown policy mode", options: []Option{WithModel(&scriptedModel{}), WithPermissionPolicy(permission.Policy{Mode: "unknown"})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent, err := New(context.Background(), test.options...)
			if err == nil {
				if agent != nil {
					_ = agent.Close()
				}
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestNewRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(ctx, WithModel(&scriptedModel{})); !errors.Is(err, context.Canceled) {
		t.Fatalf("New() error = %v, want context.Canceled", err)
	}
}

func TestPermissionOptionsKeepRuntimeAndPolicyModesConsistent(t *testing.T) {
	t.Run("explicit policy mode is selected when last", func(t *testing.T) {
		configuration := defaultConfig(t.TempDir())
		if err := WithPermissionMode(permission.CriticalOnly)(&configuration); err != nil {
			t.Fatal(err)
		}
		if err := WithPermissionPolicy(permission.Policy{Mode: permission.DontAsk})(&configuration); err != nil {
			t.Fatal(err)
		}
		if configuration.permissionMode != permission.DontAsk || configuration.permissionPolicy.Mode != permission.DontAsk {
			t.Fatalf("modes = runtime %q, policy %q", configuration.permissionMode, configuration.permissionPolicy.Mode)
		}
	})

	t.Run("mode option is selected when last", func(t *testing.T) {
		configuration := defaultConfig(t.TempDir())
		if err := WithPermissionPolicy(permission.Policy{Mode: permission.Unrestricted})(&configuration); err != nil {
			t.Fatal(err)
		}
		if err := WithPermissionMode(permission.Plan)(&configuration); err != nil {
			t.Fatal(err)
		}
		if configuration.permissionMode != permission.Plan || configuration.permissionPolicy.Mode != permission.Plan {
			t.Fatalf("modes = runtime %q, policy %q", configuration.permissionMode, configuration.permissionPolicy.Mode)
		}
	})

	t.Run("mode-less policy inherits current mode", func(t *testing.T) {
		configuration := defaultConfig(t.TempDir())
		if err := WithPermissionMode(permission.AcceptEdits)(&configuration); err != nil {
			t.Fatal(err)
		}
		if err := WithPermissionPolicy(permission.Policy{})(&configuration); err != nil {
			t.Fatal(err)
		}
		if configuration.permissionMode != permission.AcceptEdits || configuration.permissionPolicy.Mode != permission.AcceptEdits {
			t.Fatalf("modes = runtime %q, policy %q", configuration.permissionMode, configuration.permissionPolicy.Mode)
		}
	})
}

func TestNewExposesNoToolsByDefault(t *testing.T) {
	model := &scriptedModel{}
	agent, err := New(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("no-tools"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	requests := model.Requests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}
	if len(requests[0].Tools) != 0 {
		t.Fatalf("tool definitions = %#v, want none", requests[0].Tools)
	}
}

func TestNewExposesOnlyExplicitlySuppliedTools(t *testing.T) {
	model := &scriptedModel{}
	agent, err := New(context.Background(),
		WithModel(model),
		WithTool(testTool("alpha")),
		WithTool(testTool("beta")),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("custom-tools"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	requests := model.Requests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}
	names := make([]string, len(requests[0].Tools))
	for index, definition := range requests[0].Tools {
		names[index] = definition.Name
	}
	if want := []string{"alpha", "beta"}; !slices.Equal(names, want) {
		t.Fatalf("tool definitions = %v, want %v", names, want)
	}
}

func TestCustomToolExecutesAndPermissionRoundTrip(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{{ID: "call", Name: "custom", Arguments: map[string]any{}}}}
	agent, err := New(context.Background(), WithModel(model), WithTool(toolexecution.Tool{
		Definition: agentruntime.ToolDefinition{Name: "custom", InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
		Permission: toolexecution.StaticPermission(toolexecution.PermissionConfig{
			Actions: []permission.Action{permission.FilesystemWrite},
			Risk:    permission.RiskMedium,
			Reason:  "test custom permission",
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("permission"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := waitPermission(t, run)
	if err := agent.ResolvePermission(context.Background(), permission.Decision{
		PermissionID: prompt.ID,
		SessionID:    prompt.SessionID,
		TurnID:       prompt.TurnID,
		CallID:       prompt.CallID,
		Type:         permission.AllowOnce,
	}); err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	result, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].Status != agentruntime.ToolResultSucceeded {
		t.Fatalf("tool results = %#v", result.ToolResults)
	}
}

func TestCriticalOnlyAutoAllowsMediumRiskCustomTool(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{{ID: "call", Name: "guarded", Arguments: map[string]any{}}}}
	agent, err := New(context.Background(),
		WithModel(model),
		WithPermissionMode(permission.CriticalOnly),
		WithTool(toolexecution.Tool{
			Definition: agentruntime.ToolDefinition{Name: "guarded", InputSchema: agentruntime.ToolSchema{Type: "object"}},
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
			Permission: toolexecution.StaticPermission(toolexecution.PermissionConfig{
				Actions: []permission.Action{permission.ProcessExecute},
				Risk:    permission.RiskMedium,
			}),
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("critical-only-custom"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	for _, event := range run.Events() {
		if event.Type == agentruntime.AgentPermissionRequested {
			t.Fatalf("medium-risk tool unexpectedly requested permission: %+v", event.Permission)
		}
	}
	result, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].Status != agentruntime.ToolResultSucceeded {
		t.Fatalf("tool results = %#v", result.ToolResults)
	}
}

func TestNonInteractiveDeniesPermissionPrompt(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{{ID: "call", Name: "guarded", Arguments: map[string]any{}}}}
	agent, err := New(context.Background(), WithModel(model), WithNonInteractive(true), WithTool(toolexecution.Tool{
		Definition: agentruntime.ToolDefinition{Name: "guarded", InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
		Permission: func(json.RawMessage) (permission.Description, error) {
			return permission.Description{Actions: []permission.Action{permission.FilesystemWrite}, Risk: permission.RiskMedium}, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("unattended"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	result, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].Status != agentruntime.ToolResultDenied {
		t.Fatalf("tool results = %#v", result.ToolResults)
	}
}

func TestCloseIsIdempotentAndWaitObservesExecutor(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestClosedAgentRejectsOperations(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Start(context.Background(), userRequest("closed")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start() error = %v, want ErrClosed", err)
	}
	if _, _, err := agent.StartSubscribed(context.Background(), userRequest("closed")); !errors.Is(err, ErrClosed) {
		t.Fatalf("StartSubscribed() error = %v, want ErrClosed", err)
	}
	if _, _, err := agent.SendMessage(context.Background(), "closed", "hello"); !errors.Is(err, ErrClosed) {
		t.Fatalf("SendMessage() error = %v, want ErrClosed", err)
	}
	if err := agent.ResolvePermission(context.Background(), permission.Decision{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("ResolvePermission() error = %v, want ErrClosed", err)
	}
	if err := agent.SetPermissionMode(context.Background(), permission.Unrestricted); !errors.Is(err, ErrClosed) {
		t.Fatalf("SetPermissionMode() error = %v, want ErrClosed", err)
	}
}

func TestStartSubscribedReceivesRunStartedAndCoexistsWithListMessages(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, subscription, err := agent.StartSubscribed(context.Background(), userRequest("subscribed-history"))
	if err != nil {
		t.Fatal(err)
	}
	var events []agentruntime.AgentEvent
	for event := range subscription.Events {
		events = append(events, event)
	}
	if len(events) == 0 || events[0].Type != agentruntime.RunStarted {
		t.Fatalf("subscription events = %#v, want RunStarted first", events)
	}
	if !run.Done() {
		t.Fatal("run was not complete after subscription closed")
	}

	messages, err := agent.ListMessages(context.Background(), "subscribed-history")
	if err != nil {
		t.Fatal(err)
	}
	if got := messageTypes(messages); !slices.Equal(got, []agentruntime.MessageType{agentruntime.MessageTypeUser, agentruntime.MessageTypeAssistant}) {
		t.Fatalf("message types = %v", got)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ListMessages(context.Background(), "subscribed-history"); err != nil {
		t.Fatalf("ListMessages after Close() error = %v", err)
	}
}

func TestSendMessageStartsSubscribedUserTurn(t *testing.T) {
	model := &scriptedModel{}
	agent, err := New(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, subscription, err := agent.SendMessage(context.Background(), "message-session", "hello from CLI")
	if err != nil {
		t.Fatal(err)
	}
	if run.SessionID() != "message-session" {
		t.Fatalf("session ID = %q", run.SessionID())
	}
	if run.TurnID() == "" {
		t.Fatal("turn ID was not generated")
	}

	var events []AgentEvent
	for event := range subscription.Events {
		events = append(events, event)
	}
	if len(events) == 0 || events[0].Type != RunStarted {
		t.Fatalf("subscription events = %#v, want RunStarted first", events)
	}
	if events[len(events)-1].Type != RunCompleted {
		t.Fatalf("last event type = %q, want %q", events[len(events)-1].Type, RunCompleted)
	}

	messages, err := agent.ListMessages(context.Background(), "message-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v, want user and assistant", messages)
	}
	if messages[0].Type != agentruntime.MessageTypeUser || messages[0].Content != "hello from CLI" {
		t.Fatalf("user message = %#v", messages[0])
	}
	if messages[0].TurnID != run.TurnID() || messages[0].ID == "" || messages[0].CreatedAt.IsZero() {
		t.Fatalf("normalized user message = %#v", messages[0])
	}

	requests := model.Requests()
	if len(requests) != 1 || len(requests[0].Messages) != 1 || requests[0].Messages[0].Content != "hello from CLI" {
		t.Fatalf("model requests = %#v", requests)
	}
}

func TestSendMessageRejectsInvalidInput(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	if _, _, err := agent.SendMessage(context.Background(), "", "hello"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty session error = %v, want ErrInvalidRequest", err)
	}
	if _, _, err := agent.SendMessage(context.Background(), "session", " \n\t"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty message error = %v, want ErrInvalidRequest", err)
	}
}

func TestSendMessageContinuesExistingSession(t *testing.T) {
	model := &scriptedModel{}
	agent, err := New(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	for _, message := range []string{"first", "second"} {
		_, subscription, err := agent.SendMessage(context.Background(), "continued-session", message)
		if err != nil {
			t.Fatal(err)
		}
		for range subscription.Events {
		}
	}

	requests := model.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(requests))
	}
	second := requests[1].Messages
	if len(second) != 3 ||
		second[0].Type != agentruntime.MessageTypeUser || second[0].Content != "first" ||
		second[1].Type != agentruntime.MessageTypeAssistant || second[1].Content != "done" ||
		second[2].Type != agentruntime.MessageTypeUser || second[2].Content != "second" {
		t.Fatalf("second request messages = %#v", second)
	}
}

func TestListMessagesUsesDefaultStorageAndReturnsContinuationHistory(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{{ID: "call", Name: "echo", Arguments: map[string]any{}}}}
	agent, err := New(context.Background(), WithModel(model), WithTool(toolexecution.Tool{
		Definition: agentruntime.ToolDefinition{Name: "echo", InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("history"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)

	messages, err := agent.ListMessages(context.Background(), "history")
	if err != nil {
		t.Fatal(err)
	}
	if got := messageTypes(messages); !slices.Equal(got, []agentruntime.MessageType{
		agentruntime.MessageTypeUser,
		agentruntime.MessageTypeToolCall,
		agentruntime.MessageTypeToolResult,
		agentruntime.MessageTypeAssistant,
	}) {
		t.Fatalf("message types = %v", got)
	}
}

func TestListMessagesUsesCustomStorage(t *testing.T) {
	messages := &recordingMessageStorage{MessageStorage: inmemory.NewMessageStorage()}
	agent, err := New(context.Background(), WithModel(&scriptedModel{}), WithMessageStorage(messages))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	if _, err := agent.ListMessages(context.Background(), "custom"); err != nil {
		t.Fatal(err)
	}
	if messages.ListCalls() != 1 {
		t.Fatalf("custom storage List calls = %d, want 1", messages.ListCalls())
	}
}

func TestListMessagesIsSessionIsolatedAndDefensivelyCloned(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	for _, session := range []string{"one", "two"} {
		run, err := agent.Start(context.Background(), userRequest(session))
		if err != nil {
			t.Fatal(err)
		}
		waitRun(t, run)
	}

	one, err := agent.ListMessages(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := agent.ListMessages(context.Background(), "two")
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 2 || len(two) != 2 || one[0].SessionID != "one" || two[0].SessionID != "two" {
		t.Fatalf("isolated transcripts = one %#v, two %#v", one, two)
	}
	one[0].Content = "changed"
	again, err := agent.ListMessages(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Content != "go" {
		t.Fatalf("message content = %q, want defensive copy", again[0].Content)
	}
}

func TestListMessagesValidatesInputsAndWorksAfterClose(t *testing.T) {
	var nilAgent *Agent
	if _, err := nilAgent.ListMessages(context.Background(), "session"); err == nil {
		t.Fatal("nil agent ListMessages() error = nil")
	}

	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	run, err := agent.Start(context.Background(), userRequest("closed-history"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	if _, err := agent.ListMessages(nil, "closed-history"); err != nil {
		t.Fatalf("ListMessages(nil) error = %v", err)
	}
	if _, err := agent.ListMessages(context.Background(), ""); err == nil {
		t.Fatal("ListMessages(empty session) error = nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := agent.ListMessages(canceled, "closed-history"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListMessages(canceled) error = %v, want context.Canceled", err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	messages, err := agent.ListMessages(context.Background(), "closed-history")
	if err != nil {
		t.Fatalf("ListMessages after Close() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages after Close = %d, want 2", len(messages))
	}
}

func TestSetPermissionModeSupportsAllModesAndNoOps(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}), WithPermissionPolicy(permission.Policy{
		Mode:  permission.Default,
		Rules: []permission.Rule{{Actions: []permission.Action{permission.NetworkAccess}, Outcome: permission.OutcomeDeny}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	for _, mode := range []permission.Mode{permission.Default, permission.AcceptEdits, permission.CriticalOnly, permission.DontAsk, permission.Plan, permission.Unrestricted} {
		if err := agent.SetPermissionMode(context.Background(), mode); err != nil {
			t.Fatalf("SetPermissionMode(%q) error = %v", mode, err)
		}
		if got := agent.PermissionMode(); got != mode {
			t.Fatalf("PermissionMode() = %q, want %q", got, mode)
		}
	}
	if err := agent.SetPermissionMode(context.Background(), "invalid"); err == nil {
		t.Fatal("SetPermissionMode(invalid) error = nil")
	}
}

func TestCloseWinsAgainstConcurrentStart(t *testing.T) {
	messages := &blockingMessageStorage{
		MessageStorage: inmemory.NewMessageStorage(),
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	agent, err := New(context.Background(), WithModel(&scriptedModel{}), WithMessageStorage(messages))
	if err != nil {
		t.Fatal(err)
	}

	first := make(chan error, 1)
	go func() {
		_, err := agent.Start(context.Background(), userRequest("first"))
		first <- err
	}()
	select {
	case <-messages.entered:
	case <-time.After(time.Second):
		t.Fatal("first Start did not reach storage")
	}

	closed := make(chan error, 1)
	go func() { closed <- agent.Close() }()
	select {
	case <-agent.closing.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not begin")
	}

	second := make(chan error, 1)
	go func() {
		_, err := agent.Start(context.Background(), userRequest("second"))
		second <- err
	}()
	close(messages.release)

	if err := <-first; err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	select {
	case err := <-second:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("concurrent Start() error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Start did not return")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestCloseWinsAgainstConcurrentStartSubscribed(t *testing.T) {
	messages := &blockingMessageStorage{
		MessageStorage: inmemory.NewMessageStorage(),
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	agent, err := New(context.Background(), WithModel(&scriptedModel{}), WithMessageStorage(messages))
	if err != nil {
		t.Fatal(err)
	}

	first := make(chan error, 1)
	go func() {
		_, _, err := agent.StartSubscribed(context.Background(), userRequest("first"))
		first <- err
	}()
	select {
	case <-messages.entered:
	case <-time.After(time.Second):
		t.Fatal("first StartSubscribed did not reach storage")
	}

	closed := make(chan error, 1)
	go func() { closed <- agent.Close() }()
	select {
	case <-agent.closing.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not begin")
	}

	second := make(chan error, 1)
	go func() {
		_, _, err := agent.StartSubscribed(context.Background(), userRequest("second"))
		second <- err
	}()
	close(messages.release)

	if err := <-first; err != nil {
		t.Fatalf("first StartSubscribed() error = %v", err)
	}
	select {
	case err := <-second:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("concurrent StartSubscribed() error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent StartSubscribed did not return")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestStartsForDistinctSessionsDoNotSerialize(t *testing.T) {
	messages := &blockingMessageStorage{
		MessageStorage: inmemory.NewMessageStorage(),
		entered:        make(chan struct{}, 2),
		release:        make(chan struct{}),
	}
	agent, err := New(context.Background(), WithModel(&scriptedModel{}), WithMessageStorage(messages))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	starts := make(chan error, 2)
	for _, session := range []string{"one", "two"} {
		go func(session string) {
			_, err := agent.Start(context.Background(), userRequest(session))
			starts <- err
		}(session)
	}
	for range 2 {
		select {
		case <-messages.entered:
		case <-time.After(time.Second):
			t.Fatal("independent Start calls did not concurrently reach storage")
		}
	}
	close(messages.release)
	for range 2 {
		if err := <-starts; err != nil {
			t.Fatalf("Start() error = %v", err)
		}
	}
}

func TestCloseWinsAgainstConcurrentResolvePermission(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}

	// Hold the operation gate long enough to prove that a resolve waiting
	// behind shutdown observes ErrClosed rather than reaching the runtime.
	agent.operationMu.Lock()
	closed := make(chan error, 1)
	go func() { closed <- agent.Close() }()
	select {
	case <-agent.closing.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not begin")
	}
	resolved := make(chan error, 1)
	go func() { resolved <- agent.ResolvePermission(context.Background(), permission.Decision{}) }()
	agent.operationMu.Unlock()

	select {
	case err := <-resolved:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("ResolvePermission() error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent ResolvePermission did not return")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestParallelDistinctSessions(t *testing.T) {
	model := &scriptedModel{}
	agent, err := New(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	runs := make(chan *agentruntime.Run, 2)
	errors := make(chan error, 2)
	for _, session := range []string{"one", "two"} {
		go func(session string) {
			run, err := agent.Start(context.Background(), userRequest(session))
			if err != nil {
				errors <- err
				return
			}
			runs <- run
		}(session)
	}
	for range 2 {
		select {
		case err := <-errors:
			t.Fatal(err)
		case run := <-runs:
			waitRun(t, run)
		case <-time.After(time.Second):
			t.Fatal("timed out starting parallel sessions")
		}
	}
	if got := len(model.Requests()); got != 2 {
		t.Fatalf("model requests = %d, want 2", got)
	}
}

func userRequest(session string) agentruntime.Request {
	return agentruntime.Request{SessionID: session, Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "go"}}
}

func testTool(name string) toolexecution.Tool {
	return toolexecution.Tool{
		Definition: agentruntime.ToolDefinition{Name: name, InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}
}

func waitPermission(t *testing.T, run *agentruntime.Run) permission.Request {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, event := range run.Events() {
			if event.Type == agentruntime.AgentPermissionRequested && event.Permission != nil {
				return *event.Permission
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for permission")
	return permission.Request{}
}

func waitRun(t *testing.T, run *agentruntime.Run) {
	t.Helper()
	deadline := time.After(time.Second)
	for !run.Done() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for run")
		case <-time.After(time.Millisecond):
		}
	}
}

type scriptedModel struct {
	mu        sync.Mutex
	requests  []agentruntime.ModelRequest
	toolCalls []provider.ToolCall
	starts    int
}

func (m *scriptedModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	m.starts++
	if m.starts == 1 && len(m.toolCalls) != 0 {
		return scriptedStream{result: provider.StreamResult{CompletedTools: m.toolCalls, Finished: true}}, nil
	}
	return scriptedStream{result: provider.StreamResult{Content: "done", Finished: true}}, nil
}

func (m *scriptedModel) Requests() []agentruntime.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentruntime.ModelRequest(nil), m.requests...)
}

type scriptedStream struct{ result provider.StreamResult }

func (s scriptedStream) Subscribe(context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	events <- provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: s.result}}
	close(events)
	return events
}

func (scriptedStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, errors.New("unused")
}

type blockingMessageStorage struct {
	storage.MessageStorage
	entered chan struct{}
	release chan struct{}
}

type recordingMessageStorage struct {
	storage.MessageStorage
	mu        sync.Mutex
	listCalls int
}

func (s *recordingMessageStorage) List(ctx context.Context, sessionID string) ([]storage.Message, error) {
	s.mu.Lock()
	s.listCalls++
	s.mu.Unlock()
	return s.MessageStorage.List(ctx, sessionID)
}

func (s *recordingMessageStorage) ListCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls
}

func messageTypes(messages []agentruntime.Message) []agentruntime.MessageType {
	types := make([]agentruntime.MessageType, len(messages))
	for index, message := range messages {
		types[index] = message.Type
	}
	return types
}

func (s *blockingMessageStorage) TurnExists(ctx context.Context, sessionID, turnID string) (bool, error) {
	select {
	case s.entered <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return s.MessageStorage.TurnExists(ctx, sessionID, turnID)
}
