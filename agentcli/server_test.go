package agentcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-api/agentruntime"
	"harness-api/confirmation"
	"harness-api/permission"
	"harness-api/provider"
	"harness-api/toolexecution"

	"github.com/labstack/echo/v4"
)

func TestServerChatStreamStatusAndTranscript(t *testing.T) {
	agent, _, baseURL := newTestHTTPServer(t, &scriptedModel{})

	start := startHTTPRun(t, baseURL, "chat-session", `{"message":"hello"}`)
	if start.SessionID != "chat-session" || start.TurnID == "" || start.SessionEventsURL != "/v1/sessions/chat-session/events" {
		t.Fatalf("start response = %+v", start)
	}

	events := getSSEEvents(t, baseURL+start.EventsURL, "")
	if !hasServerEvent(events, agentruntime.RunStarted) || !hasServerEvent(events, agentruntime.ProviderEventReceived) || !hasServerEvent(events, agentruntime.RunCompleted) {
		t.Fatalf("events = %#v", events)
	}

	var turn TurnResponse
	getJSON(t, baseURL+start.TurnURL, &turn)
	if turn.Status != agentruntime.RunStatusDone || turn.Result == nil || turn.Result.Content != "done" {
		t.Fatalf("turn = %+v", turn)
	}

	var transcript MessagesResponse
	getJSON(t, baseURL+start.MessagesURL, &transcript)
	if len(transcript.Messages) != 2 || transcript.Messages[0].Type != agentruntime.MessageTypeUser || transcript.Messages[1].Type != agentruntime.MessageTypeAssistant {
		t.Fatalf("transcript = %+v", transcript)
	}
	if transcript.Messages[0].Content != "hello" || transcript.Messages[0].SessionID != "chat-session" {
		t.Fatalf("user message = %+v", transcript.Messages[0])
	}
	if mode := agent.PermissionMode(); mode != permission.Default {
		t.Fatalf("mode = %q", mode)
	}
}

func TestServerStreamingPOST(t *testing.T) {
	_, _, baseURL := newTestHTTPServer(t, &scriptedModel{})
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/sessions/stream-session/turns", strings.NewReader(`{"message":"stream it"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Turn-ID") == "" {
		t.Fatalf("status = %d headers = %v", response.StatusCode, response.Header)
	}
	events := decodeSSE(t, response.Body)
	if !hasServerEvent(events, agentruntime.RunStarted) || !hasServerEvent(events, agentruntime.RunCompleted) {
		t.Fatalf("events = %#v", events)
	}
}

func TestServerSessionEventsReplayAndReconnectAcrossTurns(t *testing.T) {
	_, _, baseURL := newTestHTTPServer(t, &scriptedModel{})

	first := startHTTPRun(t, baseURL, "session-feed", `{"message":"first"}`)
	getSSEEvents(t, baseURL+first.EventsURL, "")
	feedURL := baseURL + "/v1/sessions/session-feed/events"
	firstFeed := getSessionEventsUntil(t, feedURL, "", func(event SessionEventResponse) bool {
		return event.TurnID == first.TurnID && event.RuntimeEvent != nil && event.RuntimeEvent.Type == agentruntime.RunCompleted
	})
	if len(firstFeed) < 2 || firstFeed[0].Type != SessionActivityTurnAdmitted {
		t.Fatalf("first session feed = %#v", firstFeed)
	}
	for index, event := range firstFeed {
		if event.Cursor != uint64(index+1) || event.Source != ServerTurnSourceUser || event.SessionID != "session-feed" {
			t.Fatalf("first session event %d = %#v", index, event)
		}
	}
	lastCursor := firstFeed[len(firstFeed)-1].Cursor

	second := startHTTPRun(t, baseURL, "session-feed", `{"message":"second"}`)
	getSSEEvents(t, baseURL+second.EventsURL, "")
	secondFeed := getSessionEventsUntil(t, feedURL, strconv.FormatUint(lastCursor, 10), func(event SessionEventResponse) bool {
		return event.TurnID == second.TurnID && event.RuntimeEvent != nil && event.RuntimeEvent.Type == agentruntime.RunCompleted
	})
	if len(secondFeed) == 0 || secondFeed[0].Cursor != lastCursor+1 {
		t.Fatalf("reconnected session feed = %#v, last cursor %d", secondFeed, lastCursor)
	}
	for _, event := range secondFeed {
		if event.TurnID != second.TurnID {
			t.Fatalf("reconnect replayed an old turn: %#v", event)
		}
	}
}

func TestServerQueuesSameSessionTurnsAndRunsOtherSessionsInParallel(t *testing.T) {
	model := newServerQueueModel()
	_, _, baseURL := newTestHTTPServer(t, model)

	first := startHTTPRun(t, baseURL, "ordered", `{"message":"first"}`)
	model.waitStarted(t, "first")

	response := doJSON(t, http.MethodPost, baseURL+"/v1/sessions/ordered/turns", `{"message":"second"}`, "")
	if response.StatusCode != http.StatusAccepted {
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("queued status = %d body = %s", response.StatusCode, body)
	}
	var second StartTurnResponse
	if err := json.NewDecoder(response.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if second.Status != RunStatusQueued || second.QueuePosition != 1 || second.TurnID == "" {
		t.Fatalf("queued response = %+v", second)
	}

	var queued TurnResponse
	getJSON(t, baseURL+second.TurnURL, &queued)
	if queued.Status != RunStatusQueued || queued.QueuePosition != 1 {
		t.Fatalf("queued turn = %+v", queued)
	}

	parallel := startHTTPRun(t, baseURL, "parallel", `{"message":"other session"}`)
	model.waitStarted(t, "other session")
	model.release(t, "other session")
	if events := getSSEEvents(t, baseURL+parallel.EventsURL, ""); !hasServerEvent(events, agentruntime.RunCompleted) {
		t.Fatalf("parallel events = %#v", events)
	}

	streamRequest, err := http.NewRequest(http.MethodGet, baseURL+second.EventsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	streamRequest.Header.Set("Accept", "text/event-stream")
	type streamResponse struct {
		response *http.Response
		err      error
	}
	streamResponses := make(chan streamResponse, 1)
	go func() {
		streamResponseValue, streamErr := http.DefaultClient.Do(streamRequest)
		streamResponses <- streamResponse{response: streamResponseValue, err: streamErr}
	}()
	select {
	case early := <-streamResponses:
		if early.response != nil {
			early.response.Body.Close()
		}
		t.Fatalf("queued event stream returned before admission: %v", early.err)
	case <-time.After(25 * time.Millisecond):
	}

	model.release(t, "first")
	model.waitStarted(t, "second")
	streamed := <-streamResponses
	if streamed.err != nil {
		t.Fatal(streamed.err)
	}
	if streamed.response.StatusCode != http.StatusOK {
		defer streamed.response.Body.Close()
		body, _ := io.ReadAll(streamed.response.Body)
		t.Fatalf("queued stream status = %d body = %s", streamed.response.StatusCode, body)
	}
	model.release(t, "second")
	events := decodeSSE(t, streamed.response.Body)
	streamed.response.Body.Close()
	if !hasServerEvent(events, agentruntime.RunStarted) || !hasServerEvent(events, agentruntime.RunCompleted) {
		t.Fatalf("queued events = %#v", events)
	}

	if events := getSSEEvents(t, baseURL+first.EventsURL, ""); !hasServerEvent(events, agentruntime.RunCompleted) {
		t.Fatalf("first events = %#v", events)
	}
	var transcript MessagesResponse
	getJSON(t, baseURL+second.MessagesURL, &transcript)
	if len(transcript.Messages) != 4 || transcript.Messages[0].Content != "first" || transcript.Messages[1].Content != "answer:first" || transcript.Messages[2].Content != "second" || transcript.Messages[3].Content != "answer:second" {
		t.Fatalf("ordered transcript = %+v", transcript.Messages)
	}
}

func TestServerCanCancelQueuedTurnBeforeExecution(t *testing.T) {
	model := newServerQueueModel()
	_, _, baseURL := newTestHTTPServer(t, model)
	first := startHTTPRun(t, baseURL, "cancel-queue", `{"message":"active"}`)
	model.waitStarted(t, "active")

	response := doJSON(t, http.MethodPost, baseURL+"/v1/sessions/cancel-queue/turns", `{"message":"never execute"}`, "")
	if response.StatusCode != http.StatusAccepted {
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("queue status = %d body = %s", response.StatusCode, body)
	}
	var queued StartTurnResponse
	if err := json.NewDecoder(response.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	response = doJSON(t, http.MethodPost, baseURL+queued.TurnURL+"/interrupt", `{"reason":"changed my mind"}`, "")
	if response.StatusCode != http.StatusAccepted {
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("cancel status = %d body = %s", response.StatusCode, body)
	}
	response.Body.Close()

	var cancelled TurnResponse
	getJSON(t, baseURL+queued.TurnURL, &cancelled)
	if cancelled.Status != agentruntime.RunStatusDone || !strings.Contains(cancelled.Error, "queued turn cancelled") {
		t.Fatalf("cancelled turn = %+v", cancelled)
	}
	model.release(t, "active")
	getSSEEvents(t, baseURL+first.EventsURL, "")
	if model.hasStarted("never execute") {
		t.Fatal("cancelled queued turn reached the model")
	}
}

func TestServerTurnQueueLimitReturnsTooManyRequests(t *testing.T) {
	model := newServerQueueModel()
	agent, err := New(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(agent, WithServerHeartbeat(time.Millisecond), WithServerTurnQueueLimit(1))
	if err != nil {
		agent.Close()
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = server.Shutdown(context.Background())
		_ = agent.Close()
	})

	first := startHTTPRun(t, httpServer.URL, "bounded", `{"message":"active"}`)
	model.waitStarted(t, "active")
	queued := doJSON(t, http.MethodPost, httpServer.URL+"/v1/sessions/bounded/turns", `{"message":"queued"}`, "")
	if queued.StatusCode != http.StatusAccepted {
		queued.Body.Close()
		t.Fatalf("first queued status = %d", queued.StatusCode)
	}
	queued.Body.Close()
	overflow := doJSON(t, http.MethodPost, httpServer.URL+"/v1/sessions/bounded/turns", `{"message":"overflow"}`, "")
	defer overflow.Body.Close()
	if overflow.StatusCode != http.StatusTooManyRequests {
		body, _ := io.ReadAll(overflow.Body)
		t.Fatalf("overflow status = %d body = %s", overflow.StatusCode, body)
	}
	var apiError APIErrorResponse
	if err := json.NewDecoder(overflow.Body).Decode(&apiError); err != nil {
		t.Fatal(err)
	}
	if apiError.Error.Code != "turn_queue_full" {
		t.Fatalf("overflow error = %+v", apiError)
	}
	model.release(t, "active")
	getSSEEvents(t, httpServer.URL+first.EventsURL, "")
	model.waitStarted(t, "queued")
	model.release(t, "queued")
}

func TestServerPermissionCanBeAnsweredAfterEventStreamDisconnects(t *testing.T) {
	model := &scriptedModel{toolCalls: []provider.ToolCall{{ID: "call", Name: "guarded", Arguments: map[string]any{}}}}
	agent, _, baseURL := newTestHTTPServer(t, model,
		WithTool(toolexecution.Tool{
			Definition: agentruntime.ToolDefinition{Name: "guarded", InputSchema: json.RawMessage(`{"type":"object"}`)},
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
			Permission: toolexecution.StaticPermission(toolexecution.PermissionConfig{
				Actions: []permission.Action{permission.ProcessExecute},
				Risk:    permission.RiskMedium,
			}),
		}),
	)
	start := startHTTPRun(t, baseURL, "permission-session", `{"message":"use guarded"}`)

	streamContext, cancelStream := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(streamContext, http.MethodGet, baseURL+start.EventsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	var prompt EventResponse
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event EventResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == agentruntime.AgentPermissionRequested {
			prompt = event
			break
		}
	}
	cancelStream()
	_ = response.Body.Close()
	if prompt.Permission == nil {
		t.Fatal("permission event was not received")
	}

	decisionBody, err := json.Marshal(PermissionDecisionRequest{
		SessionID: prompt.SessionID,
		TurnID:    prompt.TurnID,
		CallID:    prompt.Permission.CallID,
		Decision:  permission.AllowOnce,
	})
	if err != nil {
		t.Fatal(err)
	}
	decisionURL := baseURL + "/v1/permissions/" + string(prompt.Permission.ID) + "/decisions"
	decisionResponse := doJSON(t, http.MethodPost, decisionURL, string(decisionBody), "")
	if decisionResponse.StatusCode != http.StatusOK {
		defer decisionResponse.Body.Close()
		body, _ := io.ReadAll(decisionResponse.Body)
		t.Fatalf("decision status = %d body = %s", decisionResponse.StatusCode, body)
	}
	decisionResponse.Body.Close()

	resumed := getSSEEvents(t, baseURL+start.EventsURL, strconv.FormatUint(prompt.Sequence, 10))
	if !hasServerEvent(resumed, agentruntime.AgentPermissionResolved) || !hasServerEvent(resumed, agentruntime.ToolResultReceived) || !hasServerEvent(resumed, agentruntime.RunCompleted) {
		t.Fatalf("resumed events = %#v", resumed)
	}
	if mode := agent.PermissionMode(); mode != permission.Default {
		t.Fatalf("mode = %q", mode)
	}
}

func TestServerConfirmationCanBeAnsweredYesAfterEventStreamDisconnects(t *testing.T) {
	model := newConfirmationModel()
	_, _, baseURL := newTestHTTPServer(t, model, WithTool(confirmationTool(func() {})))
	start := startHTTPRun(t, baseURL, "confirmation-session", `{"message":"publish"}`)

	streamContext, cancelStream := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(streamContext, http.MethodGet, baseURL+start.EventsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	var prompt EventResponse
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event EventResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == agentruntime.AgentConfirmationRequested {
			prompt = event
			break
		}
	}
	cancelStream()
	_ = response.Body.Close()
	if prompt.Confirmation == nil || prompt.Confirmation.Message != "Publish this report now?" {
		t.Fatalf("confirmation event = %#v", prompt)
	}

	body, err := json.Marshal(ConfirmationDecisionRequest{SessionID: prompt.SessionID, TurnID: prompt.TurnID, CallID: prompt.Confirmation.CallID, Answer: confirmation.Yes})
	if err != nil {
		t.Fatal(err)
	}
	decisionURL := baseURL + "/v1/confirmations/" + string(prompt.Confirmation.ID) + "/decisions"
	decisionResponse := doJSON(t, http.MethodPost, decisionURL, string(body), "")
	if decisionResponse.StatusCode != http.StatusOK {
		defer decisionResponse.Body.Close()
		responseBody, _ := io.ReadAll(decisionResponse.Body)
		t.Fatalf("decision status = %d body = %s", decisionResponse.StatusCode, responseBody)
	}
	decisionResponse.Body.Close()

	resumed := getSSEEvents(t, baseURL+start.EventsURL, strconv.FormatUint(prompt.Sequence, 10))
	if !hasServerEvent(resumed, agentruntime.AgentConfirmationResolved) || !hasServerEvent(resumed, agentruntime.ToolResultReceived) || !hasServerEvent(resumed, agentruntime.RunCompleted) {
		t.Fatalf("resumed events = %#v", resumed)
	}
}

func TestServerPermissionModeAndInterrupt(t *testing.T) {
	_, _, baseURL := newTestHTTPServer(t, serverBlockingModel{})

	response := doJSON(t, http.MethodPut, baseURL+"/v1/permission-mode", `{"mode":"criticalOnly"}`, "")
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("mode status = %d body = %s", response.StatusCode, body)
	}
	var mode PermissionModeResponse
	if err := json.NewDecoder(response.Body).Decode(&mode); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if mode.Previous != permission.Default || mode.Mode != permission.CriticalOnly {
		t.Fatalf("mode = %+v", mode)
	}

	start := startHTTPRun(t, baseURL, "interrupt-session", `{"message":"wait"}`)
	response = doJSON(t, http.MethodPost, baseURL+start.TurnURL+"/interrupt", `{"reason":"client stopped"}`, "")
	if response.StatusCode != http.StatusAccepted {
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("interrupt status = %d body = %s", response.StatusCode, body)
	}
	response.Body.Close()
	events := getSSEEvents(t, baseURL+start.EventsURL, "")
	if !hasServerEvent(events, agentruntime.AgentInterrupted) {
		t.Fatalf("events = %#v", events)
	}
}

func TestNewServerValidatesOptions(t *testing.T) {
	if _, err := NewServer(nil); err == nil {
		t.Fatal("NewServer(nil) error = nil")
	}
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	for _, option := range []ServerOption{
		WithServerAddress(""),
		WithServerRequestLimit(0),
		WithServerHeartbeat(0),
		WithServerTurnQueueLimit(0),
		WithServerMiddleware(nil),
	} {
		if _, err := NewServer(agent, option); err == nil {
			t.Fatalf("option %#v unexpectedly accepted", option)
		}
	}
}

func TestServerUsesEchoMiddlewareAndRejectsInvalidJSON(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	server, err := NewServer(agent, WithServerMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("X-Server-Middleware", "active")
			return next(c)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if server.Echo() == nil {
		t.Fatal("Echo() = nil")
	}
	httpServer := httptest.NewServer(server.Echo())
	defer httpServer.Close()

	response := doJSON(t, http.MethodPost, httpServer.URL+"/v1/sessions/echo-session/turns", `{"message":"hello","unexpected":true}`, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d body = %s", response.StatusCode, body)
	}
	if response.Header.Get("X-Server-Middleware") != "active" {
		t.Fatalf("middleware header = %q", response.Header.Get("X-Server-Middleware"))
	}
	var payload APIErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "invalid_json" {
		t.Fatalf("error = %+v", payload.Error)
	}
}

func TestServerRejectsRequestLargerThanConfiguredLimit(t *testing.T) {
	agent, err := New(context.Background(), WithModel(&scriptedModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	server, err := NewServer(agent, WithServerRequestLimit(16))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response := doJSON(t, http.MethodPost, httpServer.URL+"/v1/sessions/limited/turns", `{"message":"this body is too large"}`, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d body = %s", response.StatusCode, body)
	}
	var payload APIErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "request_too_large" {
		t.Fatalf("error = %+v", payload.Error)
	}
}

func newTestHTTPServer(t *testing.T, model agentruntime.Model, options ...Option) (*Agent, *Server, string) {
	t.Helper()
	agentOptions := append([]Option{WithModel(model)}, options...)
	agent, err := New(context.Background(), agentOptions...)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(agent, WithServerHeartbeat(time.Millisecond*10))
	if err != nil {
		agent.Close()
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
	return agent, server, httpServer.URL
}

func startHTTPRun(t *testing.T, baseURL, sessionID, body string) StartTurnResponse {
	t.Helper()
	response := doJSON(t, http.MethodPost, baseURL+"/v1/sessions/"+sessionID+"/turns", body, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("start status = %d body = %s", response.StatusCode, payload)
	}
	var start StartTurnResponse
	if err := json.NewDecoder(response.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	return start
}

func getSSEEvents(t *testing.T, endpoint, after string) []EventResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")
	if after != "" {
		request.Header.Set("Last-Event-ID", after)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("events status = %d body = %s", response.StatusCode, body)
	}
	return decodeSSE(t, response.Body)
}

func getSessionEventsUntil(t *testing.T, endpoint, after string, stop func(SessionEventResponse) bool) []SessionEventResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")
	if after != "" {
		request.Header.Set("Last-Event-ID", after)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("session events status = %d body = %s", response.StatusCode, body)
	}
	var events []SessionEventResponse
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event SessionEventResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		if stop(event) {
			return events
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	t.Fatalf("session event stream ended before the requested event: %#v", events)
	return nil
}

func decodeSSE(t *testing.T, reader io.Reader) []EventResponse {
	t.Helper()
	var events []EventResponse
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event EventResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func doJSON(t *testing.T, method, endpoint, body, accept string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, endpoint, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func getJSON(t *testing.T, endpoint string, target any) {
	t.Helper()
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET status = %d body = %s", response.StatusCode, body)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

type serverQueueModel struct {
	mu       sync.Mutex
	releases map[string]chan struct{}
	starts   []string
	started  chan string
}

func newServerQueueModel() *serverQueueModel {
	return &serverQueueModel{releases: make(map[string]chan struct{}), started: make(chan string, 16)}
}

func (model *serverQueueModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	prompt := ""
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if request.Messages[index].Type == agentruntime.MessageTypeUser {
			prompt = request.Messages[index].Content
			break
		}
	}
	release := make(chan struct{})
	model.mu.Lock()
	model.releases[prompt] = release
	model.starts = append(model.starts, prompt)
	model.mu.Unlock()
	model.started <- prompt
	return serverQueueStream{prompt: prompt, release: release}, nil
}

func (model *serverQueueModel) waitStarted(t *testing.T, expected string) {
	t.Helper()
	select {
	case actual := <-model.started:
		if actual != expected {
			t.Fatalf("started prompt = %q, want %q", actual, expected)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %q to start", expected)
	}
}

func (model *serverQueueModel) release(t *testing.T, prompt string) {
	t.Helper()
	model.mu.Lock()
	release := model.releases[prompt]
	delete(model.releases, prompt)
	model.mu.Unlock()
	if release == nil {
		t.Fatalf("no active model stream for %q", prompt)
	}
	close(release)
}

func (model *serverQueueModel) hasStarted(prompt string) bool {
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, started := range model.starts {
		if started == prompt {
			return true
		}
	}
	return false
}

type serverQueueStream struct {
	prompt  string
	release <-chan struct{}
}

func (stream serverQueueStream) Subscribe(ctx context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	go func() {
		defer close(events)
		select {
		case <-stream.release:
			events <- provider.StreamEvent{Type: provider.StreamCompleted, Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{Content: "answer:" + stream.prompt, Finished: true}}}
		case <-ctx.Done():
		}
	}()
	return events
}

func (serverQueueStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, errors.New("unused")
}

func hasServerEvent(events []EventResponse, eventType agentruntime.EventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

type serverBlockingModel struct{}

func (serverBlockingModel) Start(context.Context, agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	return serverBlockingStream{}, nil
}

type serverBlockingStream struct{}

func (serverBlockingStream) Subscribe(ctx context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent)
	go func() {
		<-ctx.Done()
		close(events)
	}()
	return events
}

func (serverBlockingStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{}, errors.New("blocking stream has no result")
}
