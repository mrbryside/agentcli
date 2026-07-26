package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
)

// ToolLifecycle is the single completion-policy setting for a custom tool.
// The zero value executes immediately and continues the current turn.
type ToolLifecycle string

const (
	// EndTurn requires the tool before a turn completes, executes its handler
	// immediately, and ends the turn after a successful result.
	EndTurn ToolLifecycle = "end_turn"
	// EndResponseScope requires the tool before a turn completes, stages its
	// latest invocation, and executes the handler once the originating user
	// response has no active turns or accepted subagent callbacks left.
	EndResponseScope ToolLifecycle = "end_response_scope"
)

type responseScopeKey struct {
	sessionID string
	scopeID   string
}

type responseTurnKey struct {
	sessionID string
	turnID    string
}

type responseChildKey struct {
	sessionID string
	childID   string
}

type responseScopeState uint8

const (
	responseScopeOpen responseScopeState = iota
	responseScopeFinalizing
	responseScopeFinalized
)

type responseScope struct {
	state            responseScopeState
	activeTurns      int
	pendingCallbacks int
	finalizers       map[string]deferredToolCall
	finalizerOrder   []string
}

type deferredToolCall struct {
	ctx       context.Context
	handler   Handler
	arguments json.RawMessage
}

type responseDispatch struct {
	id    string
	scope responseScopeKey
}

type callbackRecord struct {
	scope responseScopeKey
}

// ResponseScopeReservation reserves one callback continuation before the
// runtime accepts its turn. Rollback restores the pending callback when turn
// admission fails.
type ResponseScopeReservation struct {
	coordinator *ResponseScopeCoordinator
	turn        responseTurnKey
	scope       responseScopeKey
	dispatch    *responseDispatch
	committed   bool
	closed      bool
}

// ResponseScopeCoordinator correlates root user turns, callback continuation
// turns, and accepted subagent work. It is intentionally in-memory: a response
// scope is the lifecycle of one live request, not durable conversation state.
type ResponseScopeCoordinator struct {
	ctx context.Context

	mu        sync.Mutex
	scopes    map[responseScopeKey]*responseScope
	turns     map[responseTurnKey]responseScopeKey
	dispatch  map[responseChildKey][]*responseDispatch
	callbacks map[responseChildKey]map[string]callbackRecord
}

// NewResponseScopeCoordinator creates an empty response-scope coordinator.
func NewResponseScopeCoordinator(ctx context.Context) *ResponseScopeCoordinator {
	if ctx == nil {
		ctx = context.Background()
	}
	return &ResponseScopeCoordinator{
		ctx:       ctx,
		scopes:    make(map[responseScopeKey]*responseScope),
		turns:     make(map[responseTurnKey]responseScopeKey),
		dispatch:  make(map[responseChildKey][]*responseDispatch),
		callbacks: make(map[responseChildKey]map[string]callbackRecord),
	}
}

// BeginRootTurn opens a new response scope whose identity is the root turn.
func (c *ResponseScopeCoordinator) BeginRootTurn(sessionID, turnID string) error {
	if c == nil {
		return errors.New("response scope coordinator is nil")
	}
	if sessionID == "" {
		return fmt.Errorf("%w: session ID is required", agentruntime.ErrInvalidRequest)
	}
	if turnID == "" {
		return errors.New("response scope session and turn IDs are required")
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: turnID}
	scopeKey := responseScopeKey{sessionID: sessionID, scopeID: turnID}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.turns[turn]; exists {
		return fmt.Errorf("response turn %q already belongs to a scope", turnID)
	}
	if _, exists := c.scopes[scopeKey]; exists {
		return fmt.Errorf("response scope %q already exists", turnID)
	}
	c.scopes[scopeKey] = &responseScope{
		state:       responseScopeOpen,
		activeTurns: 1,
		finalizers:  make(map[string]deferredToolCall),
	}
	c.turns[turn] = scopeKey
	return nil
}

// RollbackRootTurn removes a root scope whose runtime turn was not accepted.
func (c *ResponseScopeCoordinator) RollbackRootTurn(sessionID, turnID string) {
	if c == nil {
		return
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: turnID}
	c.mu.Lock()
	defer c.mu.Unlock()
	scopeKey, found := c.turns[turn]
	if !found {
		return
	}
	delete(c.turns, turn)
	delete(c.scopes, scopeKey)
}

// RegisterDispatch reserves one subagent dispatch before child work can race
// to completion. The returned function rolls the registration back if the
// framework does not ultimately accept the work.
func (c *ResponseScopeCoordinator) RegisterDispatch(sessionID, parentTurnID, childID, dispatchID string) func() {
	if c == nil || sessionID == "" || parentTurnID == "" || childID == "" || dispatchID == "" {
		return func() {}
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: parentTurnID}
	child := responseChildKey{sessionID: sessionID, childID: childID}

	c.mu.Lock()
	scopeKey, found := c.turns[turn]
	if !found {
		c.mu.Unlock()
		return func() {}
	}
	scope := c.scopes[scopeKey]
	if scope == nil || scope.state != responseScopeOpen {
		c.mu.Unlock()
		return func() {}
	}
	dispatch := &responseDispatch{id: dispatchID, scope: scopeKey}
	c.dispatch[child] = append(c.dispatch[child], dispatch)
	scope.pendingCallbacks++
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			queue := c.dispatch[child]
			for index, candidate := range queue {
				if candidate != dispatch {
					continue
				}
				c.dispatch[child] = append(queue[:index], queue[index+1:]...)
				if len(c.dispatch[child]) == 0 {
					delete(c.dispatch, child)
				}
				if registered := c.scopes[scopeKey]; registered != nil && registered.pendingCallbacks > 0 {
					registered.pendingCallbacks--
				}
				return
			}
		})
	}
}

// ReserveCallbackTurn binds a callback continuation to the response scope
// that accepted the corresponding child dispatch.
func (c *ResponseScopeCoordinator) ReserveCallbackTurn(sessionID, continuationTurnID, childID, callbackTurnID string) (*ResponseScopeReservation, error) {
	if c == nil {
		return nil, errors.New("response scope coordinator is nil")
	}
	if sessionID == "" || continuationTurnID == "" || childID == "" || callbackTurnID == "" {
		return nil, errors.New("callback response scope identifiers are required")
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: continuationTurnID}
	child := responseChildKey{sessionID: sessionID, childID: childID}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.turns[turn]; exists {
		return nil, fmt.Errorf("response turn %q already belongs to a scope", continuationTurnID)
	}

	if seen := c.callbacks[child]; seen != nil {
		if prior, duplicate := seen[callbackTurnID]; duplicate {
			scope := c.scopes[prior.scope]
			if scope == nil {
				return nil, errors.New("callback response scope no longer exists")
			}
			scope.activeTurns++
			c.turns[turn] = prior.scope
			return &ResponseScopeReservation{
				coordinator: c,
				turn:        turn,
				scope:       prior.scope,
			}, nil
		}
	}

	queue := c.dispatch[child]
	if len(queue) == 0 {
		return nil, errors.New("subagent callback has no accepted dispatch in its response scope")
	}
	dispatch := queue[0]
	c.dispatch[child] = queue[1:]
	if len(c.dispatch[child]) == 0 {
		delete(c.dispatch, child)
	}
	scope := c.scopes[dispatch.scope]
	if scope == nil || scope.state != responseScopeOpen {
		return nil, errors.New("subagent callback response scope is not open")
	}
	if scope.pendingCallbacks == 0 {
		return nil, errors.New("subagent callback counter is inconsistent")
	}
	scope.pendingCallbacks--
	scope.activeTurns++
	c.turns[turn] = dispatch.scope
	if c.callbacks[child] == nil {
		c.callbacks[child] = make(map[string]callbackRecord)
	}
	c.callbacks[child][callbackTurnID] = callbackRecord{scope: dispatch.scope}
	return &ResponseScopeReservation{
		coordinator: c,
		turn:        turn,
		scope:       dispatch.scope,
		dispatch:    dispatch,
	}, nil
}

// Commit keeps a callback turn reservation after the runtime accepts it.
func (r *ResponseScopeReservation) Commit() {
	if r == nil || r.coordinator == nil {
		return
	}
	r.coordinator.mu.Lock()
	defer r.coordinator.mu.Unlock()
	if !r.closed {
		r.committed = true
	}
}

// Rollback restores the callback dispatch when runtime admission fails.
func (r *ResponseScopeReservation) Rollback(childID, callbackTurnID string) {
	if r == nil || r.coordinator == nil {
		return
	}
	c := r.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.closed || r.committed {
		return
	}
	r.closed = true
	delete(c.turns, r.turn)
	scope := c.scopes[r.scope]
	if scope != nil && scope.activeTurns > 0 {
		scope.activeTurns--
	}
	if r.dispatch != nil {
		child := responseChildKey{sessionID: r.turn.sessionID, childID: childID}
		c.dispatch[child] = append([]*responseDispatch{r.dispatch}, c.dispatch[child]...)
		if scope != nil {
			scope.pendingCallbacks++
		}
		if seen := c.callbacks[child]; seen != nil {
			delete(seen, callbackTurnID)
			if len(seen) == 0 {
				delete(c.callbacks, child)
			}
		}
	}
}

// StageEndResponseScope replaces the candidate for this tool and returns a
// clear successful result for the model. The handler is never called here.
func (c *ResponseScopeCoordinator) StageEndResponseScope(ctx context.Context, request agentruntime.ToolRequest, handler Handler) (json.RawMessage, error) {
	if c == nil {
		return nil, errors.New("response scope coordinator is not configured")
	}
	turn := responseTurnKey{sessionID: request.SessionID, turnID: request.TurnID}

	c.mu.Lock()
	defer c.mu.Unlock()
	scopeKey, found := c.turns[turn]
	if !found {
		return nil, errors.New("tool turn does not belong to a response scope")
	}
	scope := c.scopes[scopeKey]
	if scope == nil {
		return nil, errors.New("response scope does not exist")
	}
	if scope.state == responseScopeFinalized {
		return json.Marshal(map[string]any{
			"status":                "already_finalized",
			"delivery":              "end_response_scope",
			"retry_in_current_turn": false,
		})
	}
	if scope.state == responseScopeFinalizing {
		return json.Marshal(map[string]any{
			"status":                "finalizing",
			"delivery":              "end_response_scope",
			"retry_in_current_turn": false,
		})
	}

	_, replacing := scope.finalizers[request.Call.Name]
	if !replacing {
		scope.finalizerOrder = append(scope.finalizerOrder, request.Call.Name)
	}
	scope.finalizers[request.Call.Name] = deferredToolCall{
		ctx:       context.WithoutCancel(ctx),
		handler:   handler,
		arguments: cloneRawJSON(request.Call.Arguments),
	}
	return json.Marshal(map[string]any{
		"status":                "deferred",
		"reason":                "response_scope_active",
		"delivery":              "end_response_scope",
		"candidate":             map[bool]string{false: "scheduled", true: "replaced"}[replacing],
		"active_turns":          scope.activeTurns,
		"pending_callbacks":     scope.pendingCallbacks,
		"retry_in_current_turn": false,
	})
}

// FinishTurn closes one accepted runtime turn and executes staged finalizers
// only after the whole response scope becomes quiescent.
func (c *ResponseScopeCoordinator) FinishTurn(sessionID, turnID string) {
	if c == nil {
		return
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: turnID}

	c.mu.Lock()
	scopeKey, found := c.turns[turn]
	if !found {
		c.mu.Unlock()
		return
	}
	scope := c.scopes[scopeKey]
	if scope == nil || scope.activeTurns == 0 {
		c.mu.Unlock()
		return
	}
	scope.activeTurns--
	if scope.activeTurns != 0 || scope.pendingCallbacks != 0 || scope.state != responseScopeOpen {
		c.mu.Unlock()
		return
	}
	if len(scope.finalizers) == 0 {
		c.deleteScopeLocked(scopeKey)
		c.mu.Unlock()
		return
	}
	scope.state = responseScopeFinalizing
	calls := make([]deferredToolCall, 0, len(scope.finalizerOrder))
	for _, name := range scope.finalizerOrder {
		calls = append(calls, scope.finalizers[name])
	}
	c.mu.Unlock()

	for _, call := range calls {
		c.executeDeferred(call)
	}

	c.mu.Lock()
	if current := c.scopes[scopeKey]; current != nil {
		current.state = responseScopeFinalized
	}
	c.deleteScopeLocked(scopeKey)
	c.mu.Unlock()
}

func (c *ResponseScopeCoordinator) deleteScopeLocked(scopeKey responseScopeKey) {
	delete(c.scopes, scopeKey)
	for turn, scope := range c.turns {
		if scope == scopeKey {
			delete(c.turns, turn)
		}
	}
	for child, queue := range c.dispatch {
		kept := queue[:0]
		for _, dispatch := range queue {
			if dispatch.scope != scopeKey {
				kept = append(kept, dispatch)
			}
		}
		if len(kept) == 0 {
			delete(c.dispatch, child)
		} else {
			c.dispatch[child] = kept
		}
	}
	// Callback tombstones deliberately outlive their response scope. Without
	// them, a late replay from an older scope could consume the first pending
	// dispatch of a newer scope that happens to reuse the same child.
}

func (c *ResponseScopeCoordinator) executeDeferred(call deferredToolCall) {
	if call.handler == nil {
		return
	}
	ctx, cancel := context.WithCancel(call.ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer func() {
		stop()
		cancel()
		_ = recover()
	}()
	if c.ctx.Err() != nil {
		return
	}
	_, _ = call.handler(ctx, cloneRawJSON(call.arguments))
}
