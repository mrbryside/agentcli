package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestCustomToolConfirmationYesNoAndPermissionIndependence(t *testing.T) {
	t.Run("yes executes after a delayed answer even in unrestricted mode", func(t *testing.T) {
		model := newConfirmationModel()
		executed := make(chan struct{}, 1)
		agent, err := New(context.Background(), WithModel(model), WithPermissionMode(permission.Unrestricted), WithTool(confirmationTool(func() { executed <- struct{}{} })))
		if err != nil {
			t.Fatal(err)
		}
		defer agent.Close()

		run, err := agent.Start(context.Background(), userRequest("yes-session"))
		if err != nil {
			t.Fatal(err)
		}
		request := waitConfirmation(t, run)
		if request.Title != "Publish report" || request.Message != "Publish this report now?" || request.Details != "Destination: production" || run.Status() != agentruntime.RunStatusWaitingForConfirmation {
			t.Fatalf("confirmation request = %#v status=%s", request, run.Status())
		}
		select {
		case <-executed:
			t.Fatal("handler executed before confirmation")
		default:
		}
		time.Sleep(time.Millisecond)
		if err := agent.ResolveConfirmation(context.Background(), confirmation.Decision{ConfirmationID: request.ID, SessionID: request.SessionID, TurnID: request.TurnID, CallID: request.CallID, Answer: confirmation.Yes}); err != nil {
			t.Fatal(err)
		}
		waitRun(t, run)
		select {
		case <-executed:
		default:
			t.Fatal("confirmed handler did not execute")
		}
		result, err := run.Result()
		if err != nil || len(result.ToolResults) != 1 || result.ToolResults[0].Status != agentruntime.ToolResultSucceeded {
			t.Fatalf("result = %#v, %v", result, err)
		}
		assertConfirmationEventOrder(t, run)
	})

	t.Run("no declines without executing and model continues", func(t *testing.T) {
		model := newConfirmationModel()
		executed := false
		agent, err := New(context.Background(), WithModel(model), WithTool(confirmationTool(func() { executed = true })))
		if err != nil {
			t.Fatal(err)
		}
		defer agent.Close()
		run, err := agent.Start(context.Background(), userRequest("no-session"))
		if err != nil {
			t.Fatal(err)
		}
		request := waitConfirmation(t, run)
		if err := agent.ResolveConfirmation(context.Background(), confirmation.Decision{ConfirmationID: request.ID, SessionID: request.SessionID, TurnID: request.TurnID, CallID: request.CallID, Answer: confirmation.No}); err != nil {
			t.Fatal(err)
		}
		waitRun(t, run)
		result, err := run.Result()
		if err != nil || executed || len(result.ToolResults) != 1 || result.ToolResults[0].Status != agentruntime.ToolResultDeclined {
			t.Fatalf("executed=%v result=%#v err=%v", executed, result, err)
		}
	})

	t.Run("non-interactive declines rather than bypassing", func(t *testing.T) {
		executed := false
		agent, err := New(context.Background(), WithModel(newConfirmationModel()), WithNonInteractive(true), WithPermissionMode(permission.Unrestricted), WithTool(confirmationTool(func() { executed = true })))
		if err != nil {
			t.Fatal(err)
		}
		defer agent.Close()
		run, err := agent.Start(context.Background(), userRequest("noninteractive"))
		if err != nil {
			t.Fatal(err)
		}
		waitRun(t, run)
		result, err := run.Result()
		if err != nil || executed || len(result.ToolResults) != 1 || result.ToolResults[0].Status != agentruntime.ToolResultDeclined {
			t.Fatalf("executed=%v result=%#v err=%v", executed, result, err)
		}
	})
}

func TestCustomToolPermissionRunsBeforeIndependentConfirmation(t *testing.T) {
	tool := confirmationTool(func() {})
	tool.Permission = toolexecution.StaticPermission(toolexecution.PermissionConfig{Actions: []permission.Action{permission.NetworkAccess}, Risk: permission.RiskMedium, Reason: "publishes over the network"})
	agent, err := New(context.Background(), WithModel(newConfirmationModel()), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("permission-then-confirmation"))
	if err != nil {
		t.Fatal(err)
	}
	permissionRequest := waitPermission(t, run)
	for _, event := range run.Events() {
		if event.Type == agentruntime.AgentConfirmationRequested {
			t.Fatal("confirmation was requested before permission admission")
		}
	}
	if err := agent.ResolvePermission(context.Background(), permission.Decision{PermissionID: permissionRequest.ID, SessionID: permissionRequest.SessionID, TurnID: permissionRequest.TurnID, CallID: permissionRequest.CallID, Type: permission.AllowOnce}); err != nil {
		t.Fatal(err)
	}
	confirmationRequest := waitConfirmation(t, run)
	if err := agent.ResolveConfirmation(context.Background(), confirmation.Decision{ConfirmationID: confirmationRequest.ID, SessionID: confirmationRequest.SessionID, TurnID: confirmationRequest.TurnID, CallID: confirmationRequest.CallID, Answer: confirmation.Yes}); err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	permissionIndex, confirmationIndex := -1, -1
	for index, event := range run.Events() {
		if event.Type == agentruntime.AgentPermissionResolved {
			permissionIndex = index
		}
		if event.Type == agentruntime.AgentConfirmationRequested {
			confirmationIndex = index
		}
	}
	if permissionIndex < 0 || confirmationIndex < 0 || permissionIndex > confirmationIndex {
		t.Fatalf("permission/confirmation event order = %d/%d", permissionIndex, confirmationIndex)
	}
}

func TestCustomToolConfirmationsAreCorrelatedAcrossParallelSessionsAndCancelledOnInterrupt(t *testing.T) {
	model := newConfirmationModel()
	var mu sync.Mutex
	executed := make(map[string]bool)
	tool := confirmationTool(func() {})
	tool.Handler = func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		invocation, _ := toolexecution.InvocationFromContext(ctx)
		mu.Lock()
		executed[invocation.SessionID] = true
		mu.Unlock()
		return json.RawMessage(`{"published":true}`), nil
	}
	agent, err := New(context.Background(), WithModel(model), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	runA, err := agent.Start(context.Background(), userRequest("parallel-a"))
	if err != nil {
		t.Fatal(err)
	}
	runB, err := agent.Start(context.Background(), userRequest("parallel-b"))
	if err != nil {
		t.Fatal(err)
	}
	requestA, requestB := waitConfirmation(t, runA), waitConfirmation(t, runB)
	if requestA.ID == requestB.ID || requestA.SessionID == requestB.SessionID {
		t.Fatalf("uncorrelated requests: %#v %#v", requestA, requestB)
	}
	if err := agent.ResolveConfirmation(context.Background(), confirmation.Decision{ConfirmationID: requestB.ID, SessionID: requestB.SessionID, TurnID: requestB.TurnID, CallID: requestB.CallID, Answer: confirmation.Yes}); err != nil {
		t.Fatal(err)
	}
	if err := runA.Interrupt(context.Background(), "cancel confirmation"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, runA)
	waitRun(t, runB)
	mu.Lock()
	if executed["parallel-a"] || !executed["parallel-b"] {
		t.Fatalf("executed sessions = %#v", executed)
	}
	mu.Unlock()
	decision := confirmation.Decision{ConfirmationID: requestA.ID, SessionID: requestA.SessionID, TurnID: requestA.TurnID, CallID: requestA.CallID, Answer: confirmation.Yes}
	if err := agent.ResolveConfirmation(context.Background(), decision); !errors.Is(err, confirmation.ErrNotFound) && !errors.Is(err, confirmation.ErrClosed) {
		t.Fatalf("late interrupted answer error = %v", err)
	}
}

func confirmationTool(executed func()) toolexecution.Tool {
	return toolexecution.Tool{
		Definition: agentruntime.ToolDefinition{Name: "publish_report", InputSchema: agentruntime.ToolSchema{Type: "object"}},
		Confirmation: func(json.RawMessage) (confirmation.Description, error) {
			return confirmation.Description{Title: "Publish report", Message: "Publish this report now?", Details: "Destination: production"}, nil
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			executed()
			return json.RawMessage(`{"published":true}`), nil
		},
	}
}

type confirmationModel struct {
	mu     sync.Mutex
	starts map[string]int
}

func newConfirmationModel() *confirmationModel {
	return &confirmationModel{starts: make(map[string]int)}
}

func (model *confirmationModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	model.mu.Lock()
	starts := model.starts[request.SessionID]
	model.starts[request.SessionID]++
	model.mu.Unlock()
	result := provider.StreamResult{Content: "done", Finished: true}
	if starts == 0 {
		result.Content = ""
		result.CompletedTools = []provider.ToolCall{{ID: "publish-" + request.SessionID, Name: "publish_report", Arguments: map[string]any{}}}
	}
	return scriptedStream{result: result}, nil
}

func waitConfirmation(t *testing.T, run *agentruntime.Run) confirmation.Request {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, event := range run.Events() {
			if event.Type == agentruntime.AgentConfirmationRequested && event.Confirmation != nil {
				return *event.Confirmation
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for confirmation")
	return confirmation.Request{}
}

func assertConfirmationEventOrder(t *testing.T, run *agentruntime.Run) {
	t.Helper()
	resolved, result := -1, -1
	for index, event := range run.Events() {
		switch event.Type {
		case agentruntime.AgentConfirmationResolved:
			resolved = index
		case agentruntime.ToolResultReceived:
			result = index
		}
	}
	if resolved < 0 || result < 0 || resolved > result {
		t.Fatalf("confirmation resolution/result order = %d/%d events=%#v", resolved, result, run.Events())
	}
}
