package agentcli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"harness-api/agentruntime"
	"harness-api/storage"
)

func TestServerSubagentCRUDMessagesAndOwnership(t *testing.T) {
	childModel := &subagentGateModel{releases: make(chan struct{}, 2)}
	agent, serverURL := newTestSubagentHTTPServer(t, childModel)

	var definitions SubagentDefinitionsResponse
	getJSON(t, serverURL+"/v1/subagent-definitions", &definitions)
	if len(definitions.Definitions) != 1 || definitions.Definitions[0].Name != "researcher" || definitions.Definitions[0].Skills == nil || definitions.Definitions[0].Tools == nil {
		t.Fatalf("definitions = %#v", definitions)
	}

	response := doJSON(t, http.MethodPost, serverURL+"/v1/sessions/parent-a/subagents", `{"name":"researcher","message":"research this","label":"queue work"}`, "")
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("create status = %d body = %s", response.StatusCode, body)
	}
	var created SubagentResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if created.ID == "" || created.DisplayName == "" || created.SessionID == "" || created.ParentTurnID == "" || created.Status != storage.SubagentStatusRunning {
		t.Fatalf("created = %#v", created)
	}

	var listed SubagentsResponse
	getJSON(t, serverURL+"/v1/sessions/parent-a/subagents", &listed)
	if len(listed.Subagents) != 1 || listed.Subagents[0].ID != created.ID {
		t.Fatalf("listed = %#v", listed)
	}

	var messages SubagentMessagesResponse
	getJSON(t, serverURL+subagentPath("parent-a", created.ID)+"/messages", &messages)
	if len(messages.Messages) != 1 || messages.Messages[0].Content != "research this" {
		t.Fatalf("messages = %#v", messages)
	}
	// A UI transcript render does not consume the parent model's observation
	// cursor; only ReadSubagent is permitted to advance it.
	record, found, err := agent.subagents.store.Get(context.Background(), created.ID)
	if err != nil || !found {
		t.Fatalf("record = %#v found=%v err=%v", record, found, err)
	}
	if record.ObservedMessageID != "" || record.ObservedVersion != 0 {
		t.Fatalf("UI message read observed child activity: %#v", record)
	}

	wrong := doJSON(t, http.MethodGet, serverURL+subagentPath("parent-b", created.ID), "", "")
	defer wrong.Body.Close()
	if wrong.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(wrong.Body)
		t.Fatalf("cross-parent status = %d body = %s", wrong.StatusCode, body)
	}
	wrongPermission := doJSON(t, http.MethodPost, serverURL+subagentPath("parent-b", created.ID)+"/permissions/permission-test/decisions", `{}`, "")
	defer wrongPermission.Body.Close()
	if wrongPermission.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(wrongPermission.Body)
		t.Fatalf("cross-parent permission status = %d body = %s", wrongPermission.StatusCode, body)
	}

	closed := doJSON(t, http.MethodDelete, serverURL+subagentPath("parent-a", created.ID), "", "")
	defer closed.Body.Close()
	if closed.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(closed.Body)
		t.Fatalf("close status = %d body = %s", closed.StatusCode, body)
	}
}

func TestServerSubagentTurnSSEAndReconnect(t *testing.T) {
	childModel := &subagentGateModel{releases: make(chan struct{}, 1)}
	_, serverURL := newTestSubagentHTTPServer(t, childModel)
	created := createHTTPSubagent(t, serverURL, "parent-events", `{"name":"researcher","message":"work"}`)
	if err := childModel.waitStarts(1); err != nil {
		t.Fatal(err)
	}

	childModel.releases <- struct{}{}
	events := getSSEEvents(t, serverURL+subagentTurnPath("parent-events", created.ID, created.CurrentTurnID)+"/events", "")
	if !hasServerEvent(events, agentruntime.RunStarted) || !hasServerEvent(events, agentruntime.RunCompleted) {
		t.Fatalf("events = %#v", events)
	}
	last := events[len(events)-1].Sequence
	reconnected := getSSEEvents(t, serverURL+subagentTurnPath("parent-events", created.ID, created.CurrentTurnID)+"/events", jsonNumber(last))
	if len(reconnected) != 0 {
		t.Fatalf("reconnect after final event = %#v", reconnected)
	}

	closed := doJSON(t, http.MethodDelete, serverURL+subagentPath("parent-events", created.ID), "", "")
	defer closed.Body.Close()
	if closed.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(closed.Body)
		t.Fatalf("close status = %d body = %s", closed.StatusCode, body)
	}
	history := getSSEEvents(t, serverURL+subagentTurnPath("parent-events", created.ID, created.CurrentTurnID)+"/events", "")
	if !hasServerEvent(history, agentruntime.RunStarted) || !hasServerEvent(history, agentruntime.RunCompleted) {
		t.Fatalf("closed history events = %#v", history)
	}
}

func TestServerSubagentSendQueuesTurn(t *testing.T) {
	childModel := &subagentGateModel{releases: make(chan struct{}, 2)}
	_, serverURL := newTestSubagentHTTPServer(t, childModel)
	created := createHTTPSubagent(t, serverURL, "parent-queue", `{"name":"researcher","message":"first"}`)
	response := doJSON(t, http.MethodPost, serverURL+subagentPath("parent-queue", created.ID)+"/turns", `{"message":"second"}`, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("send status = %d body = %s", response.StatusCode, body)
	}
	var queued SubagentResponse
	if err := json.NewDecoder(response.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if queued.QueuedMessages != 1 || queued.Status != storage.SubagentStatusRunning {
		t.Fatalf("queued = %#v", queued)
	}
}

func TestServerAutomaticallyContinuesSubagentCallbackAndPublishesItToSessionEvents(t *testing.T) {
	childModel := &subagentGateModel{releases: make(chan struct{}, 1)}
	agent, serverURL := newTestSubagentHTTPServer(t, childModel)
	created := createHTTPSubagent(t, serverURL, "parent-callback", `{"name":"researcher","message":"inspect this"}`)
	if err := childModel.waitStarts(1); err != nil {
		t.Fatal(err)
	}

	childModel.releases <- struct{}{}
	events := getSessionEventsUntil(t, serverURL+"/v1/sessions/parent-callback/events", "", func(event SessionEventResponse) bool {
		return event.Source == ServerTurnSourceSubagentCallback && event.RuntimeEvent != nil && event.RuntimeEvent.Type == agentruntime.RunCompleted
	})
	var callbackTurnID string
	for _, event := range events {
		if event.Source != ServerTurnSourceSubagentCallback {
			continue
		}
		callbackTurnID = event.TurnID
		if event.SubagentCallback == nil || event.SubagentCallback.SubagentID != created.ID || event.SubagentCallback.ChildTurnID != created.CurrentTurnID {
			t.Fatalf("callback activity = %#v", event)
		}
	}
	if callbackTurnID == "" {
		t.Fatalf("callback turn was not published: %#v", events)
	}

	var turn TurnResponse
	getJSON(t, serverURL+turnPath("parent-callback", callbackTurnID), &turn)
	if turn.Status != agentruntime.RunStatusDone || turn.Result == nil {
		t.Fatalf("callback turn = %#v", turn)
	}
	messages, err := agent.ListMessages(context.Background(), "parent-callback")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Type != agentruntime.MessageTypeRuntimeEvent || messages[1].Type != agentruntime.MessageTypeAssistant {
		t.Fatalf("callback parent transcript = %#v", messages)
	}
	record, found, err := agent.subagents.store.Get(context.Background(), created.ID)
	if err != nil || !found || record.ObservedMessageID == "" {
		t.Fatalf("observed child = %#v found=%v err=%v", record, found, err)
	}
}

func createHTTPSubagent(t *testing.T, serverURL, parentSessionID, body string) SubagentResponse {
	t.Helper()
	response := doJSON(t, http.MethodPost, serverURL+"/v1/sessions/"+parentSessionID+"/subagents", body, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("create subagent status = %d body = %s", response.StatusCode, payload)
	}
	var created SubagentResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created
}

func newTestSubagentHTTPServer(t *testing.T, childModel *subagentGateModel) (*Agent, string) {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers:\n  test:\n    url: https://example.test/v1\n    api_key: test-key\n")
	writeMainAgentDefinition(t, root, "test", "root-model", "")
	writeTestFile(t, filepath.Join(root, ".agentcli", "agent", "researcher", "researcher.md"), "---\nname: researcher\ndescription: Research the assigned topic.\nprovider: test\nmodel: child-model\n---\nResearch carefully.\n")
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := New(context.Background(), WithProject(project), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	// Avoid a real provider while keeping production manager ownership and
	// mailbox behavior intact.
	agent.subagents.childFactory = func(SubagentDefinition) (*Agent, error) {
		return New(context.Background(), WithModel(childModel), WithMessageStorage(agent.messages))
	}
	server, err := NewServer(agent, WithServerHeartbeat(time.Millisecond))
	if err != nil {
		_ = agent.Close()
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		_ = agent.Close()
	})
	return agent, httpServer.URL
}

func jsonNumber(value uint64) string {
	return strconv.FormatUint(value, 10)
}
