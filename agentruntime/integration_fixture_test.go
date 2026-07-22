package agentruntime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "harness-api/agentruntime"
)

const integrationTimeout = 3 * time.Second

// integrationSSEFixture is a deterministic OpenAI-compatible streaming server.
// Each script receives the decoded request after it has been recorded.
type integrationSSEFixture struct {
	t             testing.TB
	server        *httptest.Server
	mu            sync.Mutex
	requests      []map[string]any
	requestNotify chan struct{}
	scripts       []func(http.ResponseWriter, *http.Request, map[string]any)
}

func newIntegrationSSEFixture(t testing.TB, scripts ...func(http.ResponseWriter, *http.Request, map[string]any)) *integrationSSEFixture {
	t.Helper()
	fixture := &integrationSSEFixture{t: t, scripts: scripts, requestNotify: make(chan struct{}, 1)}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *integrationSSEFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
		f.t.Errorf("decode OpenAI request: %v", err)
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	index := len(f.requests)
	f.requests = append(f.requests, cloneIntegrationJSON(decoded))
	var script func(http.ResponseWriter, *http.Request, map[string]any)
	if index < len(f.scripts) {
		script = f.scripts[index]
	}
	f.mu.Unlock()
	select {
	case f.requestNotify <- struct{}{}:
	default:
	}
	if script == nil {
		http.Error(writer, "unexpected provider request", http.StatusInternalServerError)
		return
	}
	script(writer, request, decoded)
}

func (f *integrationSSEFixture) URL() string { return f.server.URL }

func (f *integrationSSEFixture) RequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *integrationSSEFixture) Request(index int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.requests) {
		f.t.Fatalf("request index %d outside %d recorded requests", index, len(f.requests))
	}
	return cloneIntegrationJSON(f.requests[index])
}

func (f *integrationSSEFixture) WaitForRequests(count int) {
	f.t.Helper()
	deadline := time.NewTimer(integrationTimeout)
	defer deadline.Stop()
	for {
		if f.RequestCount() >= count {
			return
		}
		select {
		case <-deadline.C:
			f.t.Fatalf("timed out waiting for %d provider requests; got %d", count, f.RequestCount())
		case <-f.requestNotify:
		}
	}
}

func cloneIntegrationJSON(value map[string]any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal fixture JSON: %v", err))
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		panic(fmt.Sprintf("unmarshal fixture JSON: %v", err))
	}
	return clone
}

type integrationToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func integrationToolCallStream(calls ...integrationToolCall) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(writer http.ResponseWriter, _ *http.Request, _ map[string]any) {
		toolCalls := make([]any, len(calls))
		for index, call := range calls {
			toolCalls[index] = map[string]any{
				"index": index,
				"id":    call.ID,
				"type":  "function",
				"function": map[string]any{
					"name":      call.Name,
					"arguments": call.Arguments,
				},
			}
		}
		writeIntegrationSSE(writer, map[string]any{
			"id": "fixture-tool-calls", "object": "chat.completion.chunk", "created": 1, "model": "fixture",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": toolCalls}}},
		})
		writeIntegrationSSE(writer, map[string]any{
			"id": "fixture-tool-calls", "object": "chat.completion.chunk", "created": 1, "model": "fixture",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
		})
		writeIntegrationDone(writer)
	}
}

func integrationContentStream(content string) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(writer http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeIntegrationSSE(writer, map[string]any{
			"id": "fixture-content", "object": "chat.completion.chunk", "created": 1, "model": "fixture",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}}},
		})
		writeIntegrationSSE(writer, map[string]any{
			"id": "fixture-content", "object": "chat.completion.chunk", "created": 1, "model": "fixture",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		})
		writeIntegrationDone(writer)
	}
}

func writeIntegrationSSE(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "text/event-stream")
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal SSE event: %v", err))
	}
	_, _ = fmt.Fprintf(writer, "data: %s\n\n", encoded)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeIntegrationDone(writer http.ResponseWriter) {
	_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// integrationBarrier proves progress through a named point without relying on
// scheduling delays. Release may be called once after Wait has observed entry.
type integrationBarrier struct {
	entered      chan struct{}
	release      chan struct{}
	canceled     chan struct{}
	once         sync.Once
	canceledOnce sync.Once
}

func newIntegrationBarrier() *integrationBarrier {
	return &integrationBarrier{entered: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
}

func (b *integrationBarrier) Block(ctx context.Context) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		b.canceledOnce.Do(func() { close(b.canceled) })
		return ctx.Err()
	}
}

func (b *integrationBarrier) Wait(t testing.TB) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(integrationTimeout):
		t.Fatal("timed out waiting at integration barrier")
	}
}

func (b *integrationBarrier) Release() { close(b.release) }

func (b *integrationBarrier) WaitCanceled(t testing.TB) {
	t.Helper()
	select {
	case <-b.canceled:
	case <-time.After(integrationTimeout):
		t.Fatal("timed out waiting for integration barrier cancellation")
	}
}

// integrationGate waits for a fixed number of independent actors to enter
// before the test releases all of them together. It makes overlap assertions
// independent of scheduler timing.
type integrationGate struct {
	want        int
	mu          sync.Mutex
	entered     int
	ready       chan struct{}
	release     chan struct{}
	readyOnce   sync.Once
	releaseOnce sync.Once
}

func newIntegrationGate(want int) *integrationGate {
	return &integrationGate{want: want, ready: make(chan struct{}), release: make(chan struct{})}
}

func (g *integrationGate) Block(ctx context.Context) error {
	g.mu.Lock()
	g.entered++
	if g.entered >= g.want {
		g.readyOnce.Do(func() { close(g.ready) })
	}
	g.mu.Unlock()

	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *integrationGate) Wait(t testing.TB) {
	t.Helper()
	select {
	case <-g.ready:
	case <-time.After(integrationTimeout):
		t.Fatal("timed out waiting for integration gate")
	}
}

func (g *integrationGate) Release() { g.releaseOnce.Do(func() { close(g.release) }) }

func receiveIntegrationEvent(t testing.TB, events <-chan AgentEvent, predicate func(AgentEvent) bool) AgentEvent {
	t.Helper()
	select {
	case event, open := <-events:
		if !open {
			t.Fatal("event subscription closed before expected event")
		}
		if predicate(event) {
			return event
		}
		return receiveIntegrationEvent(t, events, predicate)
	case <-time.After(integrationTimeout):
		t.Fatal("timed out waiting for integration event")
		return AgentEvent{}
	}
}

func collectIntegrationEvents(t testing.TB, events <-chan AgentEvent) []AgentEvent {
	t.Helper()
	var collected []AgentEvent
	deadline := time.NewTimer(integrationTimeout)
	defer deadline.Stop()
	for {
		select {
		case event, open := <-events:
			if !open {
				return collected
			}
			collected = append(collected, event)
		case <-deadline.C:
			t.Fatal("timed out collecting integration events")
			return nil
		}
	}
}
