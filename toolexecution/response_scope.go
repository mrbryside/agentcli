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
	// EndResponseScope requires the tool when the originating response scope
	// is ready to end. Earlier calls are skipped; the handler executes only
	// after the initial human root action or from a callback/final repair
	// boundary.
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
	state             responseScopeState
	activeTurns       int
	pendingCallbacks  int
	pendingInputs     int
	children          map[string]int
	toolCalls         map[string]int
	endScopeCompleted map[string]struct{}
	endScopeExecuting map[string]struct{}
	canonicalMessages map[string]agentruntime.Message
	endScopeOrder     []string
}

type responseDispatch struct {
	id    string
	scope responseScopeKey
}

type callbackRecord struct {
	scope responseScopeKey
}

// ResponseScopeCleanup runs when a response scope enters its final completion
// boundary, before its EndResponseScope handlers execute. childIDs contains
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
	inline      bool
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
	callbackTurns              map[responseTurnKey]struct{}
	dispatch                   map[responseChildKey][]*responseDispatch
	callbacks                  map[responseChildKey]map[string]callbackRecord
	cancelledChildren          map[responseChildKey]struct{}
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
		ctx:               ctx,
		scopes:            make(map[responseScopeKey]*responseScope),
		turns:             make(map[responseTurnKey]responseScopeKey),
		callbackTurns:     make(map[responseTurnKey]struct{}),
		dispatch:          make(map[responseChildKey][]*responseDispatch),
		callbacks:         make(map[responseChildKey]map[string]callbackRecord),
		cancelledChildren: make(map[responseChildKey]struct{}),
		events:            newScopeEventHub(ctx),
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
		state:             responseScopeOpen,
		activeTurns:       1,
		children:          make(map[string]int),
		toolCalls:         make(map[string]int),
		endScopeCompleted: make(map[string]struct{}),
		endScopeExecuting: make(map[string]struct{}),
		canonicalMessages: make(map[string]agentruntime.Message),
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
	delete(c.callbackTurns, turn)
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
	if _, cancelled := c.cancelledChildren[child]; cancelled {
		c.mu.Unlock()
		return func() {}
	}
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

// CancelChildDispatches removes every callback obligation that has not yet
// been reserved for delivery for childID. Application-owned destructive close
// paths call this after the child is durably closed so a callback that can no
// longer arrive cannot keep any parent response scope open forever.
func (c *ResponseScopeCoordinator) CancelChildDispatches(sessionID, childID string) int {
	if c == nil || sessionID == "" || childID == "" {
		return 0
	}
	child := responseChildKey{sessionID: sessionID, childID: childID}

	c.mu.Lock()
	c.cancelledChildren[child] = struct{}{}
	queue := c.dispatch[child]
	if len(queue) == 0 {
		c.mu.Unlock()
		return 0
	}
	delete(c.dispatch, child)
	scopeIDs := make([]string, 0, len(queue))
	seenScopes := make(map[responseScopeKey]struct{}, len(queue))
	for _, dispatch := range queue {
		scope := c.scopes[dispatch.scope]
		if scope == nil {
			continue
		}
		if _, seen := seenScopes[dispatch.scope]; !seen {
			seenScopes[dispatch.scope] = struct{}{}
			scopeIDs = append(scopeIDs, dispatch.scope.scopeID)
		}
		if scope.pendingCallbacks > 0 {
			scope.pendingCallbacks--
		}
		if scope.children[childID] > 0 {
			scope.children[childID]--
			if scope.children[childID] == 0 {
				delete(scope.children, childID)
			}
		}
	}
	logger := c.logger
	c.mu.Unlock()
	sort.Strings(scopeIDs)
	if logger != nil {
		logger.DebugContext(c.ctx, "response scope callback obligations cancelled",
			"session_id", sessionID,
			"child_id", childID,
			"cancelled_dispatches", len(queue),
			"scope_ids", scopeIDs,
		)
	}
	return len(queue)
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
			c.callbackTurns[turn] = struct{}{}
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
	c.callbackTurns[turn] = struct{}{}
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

// ReserveInlineCallback binds one callback to an already-active turn in the
// same response scope. The callback obligation stays non-quiescent as a
// pending runtime input until the active run has durably appended it at a
// provider boundary.
func (c *ResponseScopeCoordinator) ReserveInlineCallback(sessionID, activeTurnID, childID, callbackTurnID string) (*ResponseScopeReservation, error) {
	if c == nil {
		return nil, errors.New("response scope coordinator is nil")
	}
	if sessionID == "" || activeTurnID == "" || childID == "" || callbackTurnID == "" {
		return nil, errors.New("inline callback response scope identifiers are required")
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: activeTurnID}
	child := responseChildKey{sessionID: sessionID, childID: childID}

	c.mu.Lock()
	defer c.mu.Unlock()
	activeScope, found := c.turns[turn]
	if !found {
		return nil, errors.New("active callback turn does not belong to a response scope")
	}
	if seen := c.callbacks[child]; seen != nil {
		if _, duplicate := seen[callbackTurnID]; duplicate {
			return nil, errors.New("subagent callback was already reserved")
		}
	}
	queue := c.dispatch[child]
	if len(queue) == 0 {
		return nil, ErrResponseScopeDispatchNotFound
	}
	dispatch := queue[0]
	if dispatch.scope != activeScope {
		return nil, errors.New("active turn belongs to a different response scope")
	}
	scope := c.scopes[dispatch.scope]
	if scope == nil || scope.state != responseScopeOpen {
		return nil, errors.New("subagent callback response scope is not open")
	}
	if scope.pendingCallbacks == 0 {
		return nil, errors.New("subagent callback counter is inconsistent")
	}
	c.dispatch[child] = queue[1:]
	if len(c.dispatch[child]) == 0 {
		delete(c.dispatch, child)
	}
	scope.pendingCallbacks--
	scope.pendingInputs++
	if c.callbacks[child] == nil {
		c.callbacks[child] = make(map[string]callbackRecord)
	}
	c.callbacks[child][callbackTurnID] = callbackRecord{scope: dispatch.scope}
	return &ResponseScopeReservation{
		coordinator: c,
		turn:        turn,
		scope:       dispatch.scope,
		dispatch:    dispatch,
		inline:      true,
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
		if r.inline {
			if scope := r.coordinator.scopes[r.scope]; scope != nil && scope.pendingInputs > 0 {
				scope.pendingInputs--
			}
		}
		r.committed = true
		r.closed = true
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
	scope := c.scopes[r.scope]
	if r.inline {
		if scope != nil && scope.pendingInputs > 0 {
			scope.pendingInputs--
		}
	} else {
		delete(c.turns, r.turn)
		delete(c.callbackTurns, r.turn)
		if scope != nil && scope.activeTurns > 0 {
			scope.activeTurns--
		}
	}
	if r.dispatch != nil {
		child := responseChildKey{sessionID: r.turn.sessionID, childID: childID}
		if _, cancelled := c.cancelledChildren[child]; !cancelled {
			c.dispatch[child] = append([]*responseDispatch{r.dispatch}, c.dispatch[child]...)
			if scope != nil {
				scope.pendingCallbacks++
			}
		}
		if seen := c.callbacks[child]; seen != nil {
			delete(seen, callbackTurnID)
			if len(seen) == 0 {
				delete(c.callbacks, child)
			}
		}
	}
}

// ReadyToEnd reports whether completing turnID would make its response scope
// quiescent. A scope already finalizing remains ready so a failed end-scope
// handler can be repaired without reopening application work.
func (c *ResponseScopeCoordinator) ReadyToEnd(sessionID, turnID string) bool {
	if c == nil {
		return false
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: turnID}
	c.mu.Lock()
	defer c.mu.Unlock()
	scopeKey, found := c.turns[turn]
	if !found {
		return false
	}
	scope := c.scopes[scopeKey]
	return scope != nil &&
		scope.state != responseScopeEnded &&
		scope.activeTurns == 1 &&
		scope.pendingCallbacks == 0 &&
		scope.pendingInputs == 0
}

// EndResponseScopeBoundaryReached allows callback continuation turns to
// deliver their final boundary tool on provider step one, while retaining the
// first-action guard for the initial human root turn.
func (c *ResponseScopeCoordinator) EndResponseScopeBoundaryReached(request agentruntime.ToolRequest) bool {
	if request.CompletionBoundary {
		return true
	}
	turn := responseTurnKey{sessionID: request.SessionID, turnID: request.TurnID}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, callbackTurn := c.callbackTurns[turn]; callbackTurn {
		return true
	}
	return request.ProviderStep > 1
}

// ReserveToolCall atomically consumes one per-response-scope call allowance.
// Admitted calls count even if the handler later fails, preventing retry loops
// from bypassing a hard scope budget.
func (c *ResponseScopeCoordinator) ReserveToolCall(sessionID, turnID, toolName string, limit int) (int, bool, error) {
	if c == nil {
		return 0, false, errors.New("response scope coordinator is nil")
	}
	if limit <= 0 {
		return 0, false, errors.New("response scope tool-call limit must be positive")
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: turnID}
	c.mu.Lock()
	defer c.mu.Unlock()
	scopeKey, found := c.turns[turn]
	if !found {
		return 0, false, errors.New("tool turn does not belong to a response scope")
	}
	scope := c.scopes[scopeKey]
	if scope == nil || scope.state == responseScopeEnded {
		return 0, false, errors.New("response scope does not exist")
	}
	used := scope.toolCalls[toolName]
	if used >= limit {
		return used, false, nil
	}
	used++
	scope.toolCalls[toolName] = used
	return used, true, nil
}

// ExecuteEndResponseScope runs handler only after the initial human root
// action, or from a callback/final repair boundary, when completing the
// current turn would end its response scope. Earlier calls are successful
// no-ops but deliberately do not satisfy the required trigger.
func (c *ResponseScopeCoordinator) ExecuteEndResponseScope(
	ctx context.Context,
	request agentruntime.ToolRequest,
	handler Handler,
	canonicalAssistantMessageParameter ...string,
) (json.RawMessage, bool, error) {
	if c == nil {
		return nil, false, errors.New("response scope coordinator is not configured")
	}
	turn := responseTurnKey{sessionID: request.SessionID, turnID: request.TurnID}
	canonicalParameter := ""
	if len(canonicalAssistantMessageParameter) > 0 {
		canonicalParameter = strings.TrimSpace(canonicalAssistantMessageParameter[0])
	}

	c.mu.Lock()
	scopeKey, found := c.turns[turn]
	if !found {
		c.mu.Unlock()
		return nil, false, errors.New("tool turn does not belong to a response scope")
	}
	scope := c.scopes[scopeKey]
	if scope == nil || scope.state == responseScopeEnded {
		c.mu.Unlock()
		return nil, false, errors.New("response scope does not exist")
	}
	_, callbackTurn := c.callbackTurns[turn]
	boundaryReached := request.CompletionBoundary || callbackTurn || request.ProviderStep > 1
	ready := boundaryReached &&
		scope.activeTurns == 1 &&
		scope.pendingCallbacks == 0 &&
		scope.pendingInputs == 0
	if !ready {
		logger := c.logger
		activeTurns := scope.activeTurns
		pendingCallbacks := scope.pendingCallbacks
		pendingInputs := scope.pendingInputs
		c.mu.Unlock()
		if logger != nil {
			logger.DebugContext(c.ctx, "response scope tool skipped",
				"session_id", request.SessionID,
				"turn_id", request.TurnID,
				"tool_name", request.Call.Name,
				"provider_step", request.ProviderStep,
				"completion_boundary", request.CompletionBoundary,
				"active_turns", activeTurns,
				"pending_callbacks", pendingCallbacks,
				"pending_inputs", pendingInputs,
			)
		}
		instruction := "This call was skipped because the tool only runs when the response scope is ready to end. " +
			"The handler did not run and the arguments were not retained. " +
			"Continue the remaining work and do not retry this tool now. " +
			"The runtime will request it again at the correct time."
		if !boundaryReached && activeTurns == 1 && pendingCallbacks == 0 && pendingInputs == 0 {
			instruction = "This call was skipped because an EndResponseScope tool cannot end the initial human root turn as its first provider action. " +
				"The handler did not run and the arguments were not retained. " +
				"Continue the remaining work and do not retry it in this provider round. " +
				"On a later provider round, call it again only after the response is complete and the scope is quiescent; " +
				"completion repair can also request the final call."
		}
		output, err := json.Marshal(map[string]any{
			"status":      "skipped",
			"executed":    false,
			"reason":      "response_scope_not_ready_to_end",
			"instruction": instruction,
		})
		return output, false, err
	}
	if _, completed := scope.endScopeCompleted[request.Call.Name]; completed {
		c.mu.Unlock()
		output, err := json.Marshal(map[string]any{
			"status":           "succeeded",
			"executed":         true,
			"already_executed": true,
		})
		return output, true, err
	}
	if _, executing := scope.endScopeExecuting[request.Call.Name]; executing {
		c.mu.Unlock()
		output, err := json.Marshal(map[string]any{
			"status":   "skipped",
			"executed": false,
			"reason":   "end_response_scope_tool_already_executing",
			"instruction": "Another invocation of this tool is already running. " +
				"Continue without retrying it.",
		})
		return output, false, err
	}

	scope.endScopeExecuting[request.Call.Name] = struct{}{}
	if !containsResponseScopeTool(scope.endScopeOrder, request.Call.Name) {
		scope.endScopeOrder = append(scope.endScopeOrder, request.Call.Name)
	}
	beginEnding := scope.state == responseScopeOpen
	var children []string
	cleanup := c.cleanup
	logger := c.logger
	if beginEnding {
		scope.state = responseScopeEnding
		children = make([]string, 0, len(scope.children))
		for childID := range scope.children {
			children = append(children, childID)
		}
		sort.Strings(children)
	}
	c.mu.Unlock()

	if beginEnding {
		c.beginEnding(scopeKey, request.TurnID, children, []string{request.Call.Name}, cleanup, logger)
	}

	output, err := handler(ctx, cloneRawJSON(request.Call.Arguments))
	if err == nil && !json.Valid(output) {
		err = errors.New("tool returned invalid JSON")
	}
	var canonicalMessage *agentruntime.Message
	if err == nil && canonicalParameter != "" {
		content, contentErr := canonicalAssistantContent(request.Call.Arguments, canonicalParameter)
		if contentErr != nil {
			err = contentErr
		} else {
			canonicalMessage = &agentruntime.Message{
				SessionID: request.SessionID,
				TurnID:    request.TurnID,
				Type:      agentruntime.MessageTypeAssistant,
				Content:   content,
			}
		}
	}

	c.mu.Lock()
	if current := c.scopes[scopeKey]; current != nil {
		delete(current.endScopeExecuting, request.Call.Name)
		if err == nil {
			current.endScopeCompleted[request.Call.Name] = struct{}{}
			if canonicalMessage != nil {
				current.canonicalMessages[request.Call.Name] = *canonicalMessage
			}
		}
	}
	c.mu.Unlock()
	if err != nil {
		if logger != nil {
			logger.ErrorContext(c.ctx, "response scope tool failed",
				"session_id", request.SessionID,
				"turn_id", request.TurnID,
				"tool_name", request.Call.Name,
				"error", err,
			)
		}
		return nil, true, err
	}
	return cloneRawJSON(output), true, nil
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
	if scope.activeTurns != 0 || scope.pendingCallbacks != 0 || scope.pendingInputs != 0 || scope.state == responseScopeEnded {
		c.mu.Unlock()
		return
	}
	beginEnding := scope.state == responseScopeOpen
	if beginEnding {
		scope.state = responseScopeEnding
	}
	children := make([]string, 0, len(scope.children))
	for childID := range scope.children {
		children = append(children, childID)
	}
	sort.Strings(children)
	cleanup := c.cleanup
	recorder := c.canonicalAssistantRecorder
	logger := c.logger
	toolNames := append([]string(nil), scope.endScopeOrder...)
	canonicalMessages := make([]agentruntime.Message, 0, len(toolNames))
	for _, name := range toolNames {
		if message, found := scope.canonicalMessages[name]; found {
			canonicalMessages = append(canonicalMessages, message)
		}
	}
	c.mu.Unlock()

	if beginEnding {
		c.beginEnding(scopeKey, turnID, children, toolNames, cleanup, logger)
	}
	if recorder != nil {
		for _, message := range canonicalMessages {
			if err := recorder(c.ctx, message); err != nil {
				if logger != nil {
					logger.ErrorContext(c.ctx, "canonical assistant message persistence failed",
						"session_id", message.SessionID,
						"turn_id", message.TurnID,
						"error", err,
					)
				}
				continue
			}
			if logger != nil {
				logger.DebugContext(c.ctx, "canonical assistant message persisted",
					"session_id", message.SessionID,
					"turn_id", message.TurnID,
				)
			}
		}
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

func (c *ResponseScopeCoordinator) beginEnding(
	scopeKey responseScopeKey,
	turnID string,
	children []string,
	toolNames []string,
	cleanup ResponseScopeCleanup,
	logger *slog.Logger,
) {
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
}

func (c *ResponseScopeCoordinator) deleteScopeLocked(scopeKey responseScopeKey) {
	delete(c.scopes, scopeKey)
	for turn, scope := range c.turns {
		if scope == scopeKey {
			delete(c.turns, turn)
			delete(c.callbackTurns, turn)
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

func containsResponseScopeTool(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}
