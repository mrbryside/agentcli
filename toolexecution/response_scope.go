package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
)

// ErrResponseScopeDispatchNotFound means a child callback did not originate
// from work accepted by a live response scope. Direct application-created
// children can legitimately have no such dispatch.
var ErrResponseScopeDispatchNotFound = errors.New("subagent callback has no accepted dispatch in its response scope")

// ToolTrigger groups a custom tool's required-completion and handler-delivery
// behavior into one setting. It does not control whether the current turn
// ends; configure Tool.EndTurnOnSuccess independently.
type ToolTrigger string

const (
	// EndTurn requires the tool before a turn completes and executes its handler
	// immediately.
	EndTurn ToolTrigger = "end_turn"
	// EndResponseScope requires the tool before a turn completes, stages its
	// latest invocation, and executes the handler once the originating user
	// response has no active turns or accepted subagent callbacks left.
	EndResponseScope ToolTrigger = "end_response_scope"
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
	responseScopeEnding
	responseScopeEnded
)

type responseScope struct {
	state            responseScopeState
	activeTurns      int
	pendingCallbacks int
	children         map[string]int
	endScopeCalls    map[string]endScopeToolCall
	endScopeOrder    []string
}

type endScopeToolCall struct {
	ctx                                context.Context
	handler                            Handler
	request                            agentruntime.ToolRequest
	arguments                          json.RawMessage
	canonicalAssistantMessageParameter string
}

type responseDispatch struct {
	id    string
	scope responseScopeKey
}

type callbackRecord struct {
	scope responseScopeKey
}

// ResponseScopeCleanup runs after a response scope becomes quiescent and
// before its deferred EndResponseScope handlers execute. childIDs contains
// every child that accepted work in the scope.
type ResponseScopeCleanup func(context.Context, string, string, []string)

// CanonicalAssistantRecorder persists the user-visible assistant response
// represented by a successfully delivered EndResponseScope tool.
type CanonicalAssistantRecorder func(context.Context, agentruntime.Message) error

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

	mu                         sync.Mutex
	scopes                     map[responseScopeKey]*responseScope
	turns                      map[responseTurnKey]responseScopeKey
	dispatch                   map[responseChildKey][]*responseDispatch
	callbacks                  map[responseChildKey]map[string]callbackRecord
	cleanup                    ResponseScopeCleanup
	canonicalAssistantRecorder CanonicalAssistantRecorder
	events                     *scopeEventHub
	logger                     *slog.Logger
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
		events:    newScopeEventHub(ctx),
	}
}

// SetLogger enables structured response-scope lifecycle logging. Passing nil
// disables it. Configure this before accepting turns.
func (c *ResponseScopeCoordinator) SetLogger(logger *slog.Logger) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.logger = logger
	c.mu.Unlock()
}

// SetCleanup installs the runtime-owned response-scope cleanup hook.
func (c *ResponseScopeCoordinator) SetCleanup(cleanup ResponseScopeCleanup) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.cleanup = cleanup
	c.mu.Unlock()
}

// SetCanonicalAssistantRecorder installs the durable transcript hook used by
// EndResponseScope tools that declare a canonical assistant message parameter.
func (c *ResponseScopeCoordinator) SetCanonicalAssistantRecorder(recorder CanonicalAssistantRecorder) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.canonicalAssistantRecorder = recorder
	c.mu.Unlock()
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
	if _, exists := c.turns[turn]; exists {
		c.mu.Unlock()
		return fmt.Errorf("response turn %q already belongs to a scope", turnID)
	}
	if _, exists := c.scopes[scopeKey]; exists {
		c.mu.Unlock()
		return fmt.Errorf("response scope %q already exists", turnID)
	}
	c.scopes[scopeKey] = &responseScope{
		state:         responseScopeOpen,
		activeTurns:   1,
		children:      make(map[string]int),
		endScopeCalls: make(map[string]endScopeToolCall),
	}
	c.turns[turn] = scopeKey
	logger := c.logger
	c.mu.Unlock()
	if logger != nil {
		logger.InfoContext(c.ctx, "response scope started",
			"session_id", sessionID,
			"scope_id", turnID,
			"trigger_turn_id", turnID,
		)
	}
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
	scope.children[childID]++
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
					registered.children[childID]--
					if registered.children[childID] == 0 {
						delete(registered.children, childID)
					}
				}
				return
			}
		})
	}
}

// ChildExclusiveToScope reports whether no other live response scope has
// accepted work for childID. Cleanup callers use it while holding the child's
// own lifecycle lock so a concurrent follow-up cannot race an automatic close.
func (c *ResponseScopeCoordinator) ChildExclusiveToScope(sessionID, scopeID, childID string) bool {
	if c == nil || sessionID == "" || scopeID == "" || childID == "" {
		return false
	}
	scopeKey := responseScopeKey{sessionID: sessionID, scopeID: scopeID}
	c.mu.Lock()
	defer c.mu.Unlock()
	scope := c.scopes[scopeKey]
	if scope == nil || scope.state != responseScopeEnding {
		return false
	}
	for candidateKey, candidate := range c.scopes {
		if candidateKey == scopeKey || candidateKey.sessionID != sessionID || candidate == nil {
			continue
		}
		if candidate.children[childID] > 0 {
			return false
		}
	}
	return true
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
		return nil, ErrResponseScopeDispatchNotFound
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
func (c *ResponseScopeCoordinator) StageEndResponseScope(ctx context.Context, request agentruntime.ToolRequest, handler Handler, canonicalAssistantMessageParameter ...string) (json.RawMessage, error) {
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
	if scope.state == responseScopeEnded {
		return json.Marshal(map[string]any{
			"status":                "already_ended",
			"delivery":              "end_response_scope",
			"retry_in_current_turn": false,
		})
	}
	if scope.state == responseScopeEnding {
		return json.Marshal(map[string]any{
			"status":                "ending",
			"delivery":              "end_response_scope",
			"retry_in_current_turn": false,
		})
	}

	_, replacing := scope.endScopeCalls[request.Call.Name]
	if !replacing {
		scope.endScopeOrder = append(scope.endScopeOrder, request.Call.Name)
	}
	canonicalParameter := ""
	if len(canonicalAssistantMessageParameter) > 0 {
		canonicalParameter = strings.TrimSpace(canonicalAssistantMessageParameter[0])
	}
	scope.endScopeCalls[request.Call.Name] = endScopeToolCall{
		ctx:                                context.WithoutCancel(ctx),
		handler:                            handler,
		request:                            cloneRequest(request),
		arguments:                          cloneRawJSON(request.Call.Arguments),
		canonicalAssistantMessageParameter: canonicalParameter,
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

// FinishTurn closes one accepted runtime turn and ends the response scope only
// after the whole scope becomes quiescent.
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
	scope.state = responseScopeEnding
	children := make([]string, 0, len(scope.children))
	for childID := range scope.children {
		children = append(children, childID)
	}
	sort.Strings(children)
	cleanup := c.cleanup
	canonicalAssistantRecorder := c.canonicalAssistantRecorder
	logger := c.logger
	toolNames := append([]string(nil), scope.endScopeOrder...)
	calls := make([]endScopeToolCall, 0, len(toolNames))
	for _, name := range toolNames {
		calls = append(calls, scope.endScopeCalls[name])
	}
	c.mu.Unlock()

	if logger != nil {
		logger.InfoContext(c.ctx, "response scope ending",
			"session_id", scopeKey.sessionID,
			"scope_id", scopeKey.scopeID,
			"trigger_turn_id", turnID,
		)
		logger.DebugContext(c.ctx, "response scope ending details",
			"session_id", scopeKey.sessionID,
			"scope_id", scopeKey.scopeID,
			"trigger_turn_id", turnID,
			"child_ids", children,
			"tool_names", toolNames,
		)
	}
	c.publishEvent(ScopeEvent{
		Type: PreEndScope, SessionID: scopeKey.sessionID, ScopeID: scopeKey.scopeID,
		TriggerTurnID: turnID, ChildIDs: children, ToolNames: toolNames,
	})
	if cleanup != nil {
		c.executeCleanup(cleanup, scopeKey, children)
	}
	for _, call := range calls {
		c.executeDeferred(call, canonicalAssistantRecorder, logger)
	}

	c.mu.Lock()
	if current := c.scopes[scopeKey]; current != nil {
		current.state = responseScopeEnded
	}
	c.deleteScopeLocked(scopeKey)
	c.mu.Unlock()
	if logger != nil {
		logger.InfoContext(c.ctx, "response scope ended",
			"session_id", scopeKey.sessionID,
			"scope_id", scopeKey.scopeID,
			"trigger_turn_id", turnID,
		)
		logger.DebugContext(c.ctx, "response scope ended details",
			"session_id", scopeKey.sessionID,
			"scope_id", scopeKey.scopeID,
			"trigger_turn_id", turnID,
			"child_ids", children,
			"tool_names", toolNames,
		)
	}
	c.publishEvent(ScopeEvent{
		Type: EndScope, SessionID: scopeKey.sessionID, ScopeID: scopeKey.scopeID,
		TriggerTurnID: turnID, ChildIDs: children, ToolNames: toolNames,
	})
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

func (c *ResponseScopeCoordinator) executeCleanup(cleanup ResponseScopeCleanup, scope responseScopeKey, children []string) {
	if cleanup == nil {
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer func() {
		stop()
		cancel()
		_ = recover()
	}()
	if c.ctx.Err() != nil {
		return
	}
	cleanup(ctx, scope.sessionID, scope.scopeID, append([]string(nil), children...))
}

func (c *ResponseScopeCoordinator) executeDeferred(call endScopeToolCall, recorder CanonicalAssistantRecorder, logger *slog.Logger) {
	if call.handler == nil {
		return
	}
	ctx, cancel := context.WithCancel(call.ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer func() {
		stop()
		cancel()
		if recovered := recover(); recovered != nil && logger != nil {
			logger.ErrorContext(c.ctx, "response scope tool failed",
				"session_id", call.request.SessionID,
				"turn_id", call.request.TurnID,
				"tool_name", call.request.Call.Name,
				"error", fmt.Sprint(recovered),
			)
		}
	}()
	if c.ctx.Err() != nil {
		return
	}
	_, err := call.handler(ctx, cloneRawJSON(call.arguments))
	if err != nil {
		if logger != nil {
			logger.ErrorContext(c.ctx, "response scope tool failed",
				"session_id", call.request.SessionID,
				"turn_id", call.request.TurnID,
				"tool_name", call.request.Call.Name,
				"error", err,
			)
		}
		return
	}
	if call.canonicalAssistantMessageParameter == "" || recorder == nil {
		return
	}
	content, err := canonicalAssistantContent(call.arguments, call.canonicalAssistantMessageParameter)
	if err != nil {
		if logger != nil {
			logger.ErrorContext(c.ctx, "canonical assistant message extraction failed",
				"session_id", call.request.SessionID,
				"turn_id", call.request.TurnID,
				"tool_name", call.request.Call.Name,
				"error", err,
			)
		}
		return
	}
	message := agentruntime.Message{
		SessionID: call.request.SessionID,
		TurnID:    call.request.TurnID,
		Type:      agentruntime.MessageTypeAssistant,
		Content:   content,
	}
	if err := recorder(ctx, message); err != nil {
		if logger != nil {
			logger.ErrorContext(c.ctx, "canonical assistant message persistence failed",
				"session_id", call.request.SessionID,
				"turn_id", call.request.TurnID,
				"tool_name", call.request.Call.Name,
				"error", err,
			)
		}
		return
	}
	if logger != nil {
		logger.DebugContext(c.ctx, "canonical assistant message persisted",
			"session_id", call.request.SessionID,
			"turn_id", call.request.TurnID,
			"tool_name", call.request.Call.Name,
		)
	}
}

func canonicalAssistantContent(arguments json.RawMessage, parameter string) (string, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &values); err != nil {
		return "", fmt.Errorf("decode tool arguments: %w", err)
	}
	raw, exists := values[parameter]
	if !exists {
		return "", fmt.Errorf("tool arguments do not contain %q", parameter)
	}
	var content string
	if err := json.Unmarshal(raw, &content); err != nil {
		return "", fmt.Errorf("decode tool argument %q: %w", parameter, err)
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("tool argument %q is empty", parameter)
	}
	return content, nil
}
