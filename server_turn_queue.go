package agentcli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
)

var (
	errServerTurnQueueFull = errors.New("server turn queue is full")
	errQueuedTurnCancelled = errors.New("queued turn cancelled")
)

// serverTurn is the HTTP lifecycle record that exists before an
// agentruntime.Run can start. Runtime itself deliberately rejects overlapping
// turns; the server serializes accepted requests before calling Agent.Start.
type serverTurn struct {
	mu       sync.RWMutex
	request  agentruntime.Request
	source   ServerTurnSource
	callback *SubagentCallback
	ready    chan struct{}
	once     sync.Once
	run      *agentruntime.Run
	err      error
}

func newServerTurn(request agentruntime.Request, source ServerTurnSource, callback *SubagentCallback) *serverTurn {
	return &serverTurn{request: request, source: source, callback: callback, ready: make(chan struct{})}
}

func (turn *serverTurn) resolve(run *agentruntime.Run, err error) {
	turn.once.Do(func() {
		turn.mu.Lock()
		turn.run = run
		turn.err = err
		turn.mu.Unlock()
		close(turn.ready)
	})
}

func (turn *serverTurn) snapshot() (*agentruntime.Run, error) {
	turn.mu.RLock()
	defer turn.mu.RUnlock()
	return turn.run, turn.err
}

func (turn *serverTurn) wait(ctx context.Context) (*agentruntime.Run, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-turn.ready:
		return turn.snapshot()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (server *Server) submitTurn(ctx context.Context, request agentruntime.Request) (*serverTurn, bool, error) {
	return server.submitTurnWithSource(ctx, request, ServerTurnSourceUser, nil, false)
}

func (server *Server) submitTurnWithSource(ctx context.Context, request agentruntime.Request, source ServerTurnSource, callback *SubagentCallback, priority bool) (*serverTurn, bool, error) {
	if request.TurnID == "" {
		turnID, err := newSubagentID("turn_")
		if err != nil {
			return nil, false, fmt.Errorf("generate turn ID: %w", err)
		}
		request.TurnID = turnID
	}
	exists, err := server.agent.messages.TurnExists(ctx, request.SessionID, request.TurnID)
	if err != nil {
		return nil, false, fmt.Errorf("check existing turn: %w", err)
	}
	if exists {
		return nil, false, agentruntime.ErrTurnExists
	}

	if source == "" {
		source = ServerTurnSourceUser
	}
	turn := newServerTurn(request, source, callback)
	key := serverRunKey{sessionID: request.SessionID, turnID: request.TurnID}
	server.runsMu.Lock()
	if _, found := server.turns[key]; found {
		server.runsMu.Unlock()
		return nil, false, agentruntime.ErrTurnExists
	}
	if server.activeTurns[request.SessionID] != nil {
		pending := server.pendingTurns[request.SessionID]
		if !priority && len(pending) >= server.config.turnQueue {
			server.runsMu.Unlock()
			return nil, false, errServerTurnQueueFull
		}
		server.turns[key] = turn
		if priority {
			server.pendingTurns[request.SessionID] = append([]*serverTurn{turn}, pending...)
		} else {
			server.pendingTurns[request.SessionID] = append(pending, turn)
		}
		queuePosition := 1
		if !priority {
			queuePosition = len(pending) + 1
		}
		server.runsMu.Unlock()
		server.sessionEvents.publish(newSessionLifecycleEvent(turn, SessionActivityTurnQueued, queuePosition, ""))
		return turn, true, nil
	}
	server.turns[key] = turn
	server.activeTurns[request.SessionID] = turn
	server.runsMu.Unlock()

	server.sessionEvents.publish(newSessionLifecycleEvent(turn, SessionActivityTurnAdmitted, 0, ""))
	if err := server.startAcceptedTurn(turn); err != nil {
		return turn, false, err
	}
	return turn, false, nil
}

func (server *Server) startAcceptedTurn(turn *serverTurn) error {
	run, err := server.agent.Start(server.context, turn.request)
	turn.resolve(run, err)
	if err != nil {
		server.sessionEvents.publish(newSessionLifecycleEvent(turn, SessionActivityTurnRejected, 0, err.Error()))
		server.advanceSession(turn)
		return err
	}
	if turn.callback != nil && server.agent.subagents != nil {
		_ = server.agent.subagents.observeCallback(context.WithoutCancel(server.context), *turn.callback)
	}
	go server.watchAcceptedTurn(turn, run)
	return nil
}

func (server *Server) watchAcceptedTurn(turn *serverTurn, run *agentruntime.Run) {
	subscription := run.Subscribe(server.context)
	backfill, err := run.EventsBetween(agentruntime.EventCursor{}, subscription.Cursor)
	if err == nil {
		for _, event := range backfill {
			server.sessionEvents.publish(newSessionRuntimeEvent(turn, event))
		}
	}
	for event := range subscription.Events {
		server.sessionEvents.publish(newSessionRuntimeEvent(turn, event))
	}
	server.advanceSession(turn)
}

func (server *Server) advanceSession(completed *serverTurn) {
	sessionID := completed.request.SessionID
	for {
		var next *serverTurn
		server.runsMu.Lock()
		if server.activeTurns[sessionID] != completed {
			server.runsMu.Unlock()
			return
		}
		delete(server.activeTurns, sessionID)
		pending := server.pendingTurns[sessionID]
		if shutdownErr := server.context.Err(); shutdownErr != nil {
			delete(server.pendingTurns, sessionID)
			server.runsMu.Unlock()
			for _, candidate := range pending {
				candidate.resolve(nil, shutdownErr)
			}
			return
		}
		for len(pending) > 0 {
			candidate := pending[0]
			pending = pending[1:]
			if run, candidateErr := candidate.snapshot(); run == nil && candidateErr == nil {
				next = candidate
				break
			}
		}
		if len(pending) == 0 {
			delete(server.pendingTurns, sessionID)
		} else {
			server.pendingTurns[sessionID] = pending
		}
		if next != nil {
			server.activeTurns[sessionID] = next
		}
		server.runsMu.Unlock()

		if next == nil {
			return
		}
		server.sessionEvents.publish(newSessionLifecycleEvent(next, SessionActivityTurnAdmitted, 0, ""))
		if err := server.startAcceptedTurn(next); err != nil {
			// startAcceptedTurn already advances this session after recording the
			// error. Avoid a second advance from this stack frame.
			return
		}
		return
	}
}

func (server *Server) queuePosition(turn *serverTurn) int {
	server.runsMu.RLock()
	defer server.runsMu.RUnlock()
	for index, candidate := range server.pendingTurns[turn.request.SessionID] {
		if candidate == turn {
			return index + 1
		}
	}
	return 0
}

func (server *Server) cancelQueuedTurn(turn *serverTurn, reason string) bool {
	server.runsMu.Lock()
	if server.activeTurns[turn.request.SessionID] == turn {
		server.runsMu.Unlock()
		return false
	}
	found := false
	pending := server.pendingTurns[turn.request.SessionID]
	for index, candidate := range pending {
		if candidate != turn {
			continue
		}
		pending = append(pending[:index], pending[index+1:]...)
		found = true
		break
	}
	if len(pending) == 0 {
		delete(server.pendingTurns, turn.request.SessionID)
	} else {
		server.pendingTurns[turn.request.SessionID] = pending
	}
	server.runsMu.Unlock()
	if !found {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "interrupted by client"
	}
	turn.resolve(nil, fmt.Errorf("%w: %s", errQueuedTurnCancelled, reason))
	server.sessionEvents.publish(newSessionLifecycleEvent(turn, SessionActivityTurnCancelled, 0, reason))
	return true
}
