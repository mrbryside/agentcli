package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
)

// ErrResponseScopeAssignmentNotFound means a subagent result did not originate
// from work accepted by a live response scope. Direct application-created
// subagentIDs can legitimately have no such assignments.
var ErrResponseScopeAssignmentNotFound = errors.New("subagent result has no accepted assignments in its response scope")

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
	// after the initial human main-agent action or from a result/final repair
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

type responseSubagentKey struct {
	sessionID  string
	subagentID string
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
	pendingResults    int
	pendingInputs     int
	deliveredResults  []*ResponseScopeDeliveredResult
	subagentCounts    map[string]int
	failedRecoveries  map[string]struct{}
	toolCalls         map[string]int
	endScopeCompleted map[string]struct{}
	endScopeExecuting map[string]struct{}
	endScopeOrder     []string
}

type responseAssignment struct {
	id       string
	scope    responseScopeKey
	sequence uint64
	pending  ResponseScopePendingResult
}

type resultRecord struct {
	scope    responseScopeKey
	received *ResponseScopeDeliveredResult
}

// ResponseScopePendingResult identifies one accepted subagent assignment whose
// result has not yet been reserved for delivery. SubagentTurnID is omitted when
// queued subagent work has not received a runtime turn ID yet.
type ResponseScopePendingResult struct {
	SubagentID     string `json:"subagent_id"`
	DefinitionName string `json:"definition_name,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	AssignmentID   string `json:"assignment_id"`
	SubagentTurnID string `json:"subagent_turn_id,omitempty"`
}

// ResponseScopeDeliveredResult identifies one result reserved in the
// response scope and records the subagent's authoritative semantic result.
type ResponseScopeDeliveredResult struct {
	SubagentID     string `json:"subagent_id"`
	DefinitionName string `json:"definition_name,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	AssignmentID   string `json:"assignment_id,omitempty"`
	SubagentTurnID string `json:"subagent_turn_id"`
	ResultStatus   string `json:"result_status"`
}

// ResponseScopeResultProgress is an atomic result-accounting snapshot
// captured for one trusted result runtime message.
type ResponseScopeResultProgress struct {
	PendingCount        int                            `json:"pending_count"`
	AllResultsDelivered bool                           `json:"all_results_delivered"`
	PendingResults      []ResponseScopePendingResult   `json:"pending_results"`
	DeliveredResults    []ResponseScopeDeliveredResult `json:"delivered_results"`
}

// ResponseScopeCleanup runs when a response scope enters its final completion
// boundary, before its EndResponseScope handlers execute. subagentIDs contains
// every subagent that accepted work in the scope.
type ResponseScopeCleanup func(context.Context, string, string, []string)

// ResponseScopeReservation reserves one result continuation before the
// runtime accepts its turn. Rollback restores the pending result when turn
// admission fails.
type ResponseScopeReservation struct {
	coordinator *ResponseScopeCoordinator
	turn        responseTurnKey
	scope       responseScopeKey
	assignments *responseAssignment
	inline      bool
	committed   bool
	closed      bool
}

// ResponseScopeCoordinator correlates main-agent user turns, result continuation
// turns, and accepted subagent work. It is intentionally in-memory: a response
// scope is the lifecycle of one live request, not durable conversation state.
type ResponseScopeCoordinator struct {
	ctx context.Context

	mu                 sync.Mutex
	scopes             map[responseScopeKey]*responseScope
	turns              map[responseTurnKey]responseScopeKey
	resultTurns        map[responseTurnKey]struct{}
	assignments        map[responseSubagentKey][]*responseAssignment
	results            map[responseSubagentKey]map[string]resultRecord
	cancelledSubagents map[responseSubagentKey]struct{}
	cleanup            ResponseScopeCleanup
	events             *scopeEventHub
	logger             *slog.Logger
	nextAssignment     uint64
}

// NewResponseScopeCoordinator creates an empty response-scope coordinator.
func NewResponseScopeCoordinator(ctx context.Context) *ResponseScopeCoordinator {
	if ctx == nil {
		ctx = context.Background()
	}
	return &ResponseScopeCoordinator{
		ctx:                ctx,
		scopes:             make(map[responseScopeKey]*responseScope),
		turns:              make(map[responseTurnKey]responseScopeKey),
		resultTurns:        make(map[responseTurnKey]struct{}),
		assignments:        make(map[responseSubagentKey][]*responseAssignment),
		results:            make(map[responseSubagentKey]map[string]resultRecord),
		cancelledSubagents: make(map[responseSubagentKey]struct{}),
		events:             newScopeEventHub(ctx),
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

// BeginMainAgentTurn opens a new response scope whose identity is the main-agent turn.
func (c *ResponseScopeCoordinator) BeginMainAgentTurn(sessionID, turnID string) error {
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
		subagentCounts:    make(map[string]int),
		failedRecoveries:  make(map[string]struct{}),
		toolCalls:         make(map[string]int),
		endScopeCompleted: make(map[string]struct{}),
		endScopeExecuting: make(map[string]struct{}),
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

// ReserveFailedRecovery allows one recovery assignments for the same subagent and
// normalized failure within a live response scope. The returned rollback
// releases the reservation when assignments admission fails. Calls outside a
// tracked response scope remain allowed because no scope lifecycle exists to
// own a budget.
func (c *ResponseScopeCoordinator) ReserveFailedRecovery(sessionID, mainAgentTurnID, subagentID, failureFingerprint string) (bool, func()) {
	if c == nil || sessionID == "" || mainAgentTurnID == "" || subagentID == "" || failureFingerprint == "" {
		return true, func() {}
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: mainAgentTurnID}
	key := subagentID + "\x00" + failureFingerprint

	c.mu.Lock()
	scopeKey, found := c.turns[turn]
	if !found {
		c.mu.Unlock()
		return true, func() {}
	}
	scope := c.scopes[scopeKey]
	if scope == nil || scope.state != responseScopeOpen {
		c.mu.Unlock()
		return false, func() {}
	}
	if _, exhausted := scope.failedRecoveries[key]; exhausted {
		c.mu.Unlock()
		return false, func() {}
	}
	scope.failedRecoveries[key] = struct{}{}
	c.mu.Unlock()

	var once sync.Once
	return true, func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if current := c.scopes[scopeKey]; current != nil {
				delete(current.failedRecoveries, key)
			}
		})
	}
}

// RollbackMainAgentTurn removes a main-agent scope whose runtime turn was not accepted.
func (c *ResponseScopeCoordinator) RollbackMainAgentTurn(sessionID, turnID string) {
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
	delete(c.resultTurns, turn)
	delete(c.scopes, scopeKey)
}

// RegisterAssignment reserves one subagent assignment before subagent work can race
// to completion. The returned function rolls the registration back if the
// framework does not ultimately accept the work.
func (c *ResponseScopeCoordinator) RegisterAssignment(sessionID, mainAgentTurnID, subagentID, assignmentID string) func() {
	return c.RegisterAssignmentMetadata(sessionID, mainAgentTurnID, ResponseScopePendingResult{
		SubagentID:   subagentID,
		AssignmentID: assignmentID,
	})
}

// RegisterAssignmentMetadata reserves one subagent result obligation and
// retains the subagent identity needed to describe pending results to the
// main agent. The returned function rolls the registration back if assignment fails.
func (c *ResponseScopeCoordinator) RegisterAssignmentMetadata(sessionID, mainAgentTurnID string, pending ResponseScopePendingResult) func() {
	subagentID := pending.SubagentID
	assignmentID := pending.AssignmentID
	if c == nil || sessionID == "" || mainAgentTurnID == "" || subagentID == "" || assignmentID == "" {
		return func() {}
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: mainAgentTurnID}
	subagent := responseSubagentKey{sessionID: sessionID, subagentID: subagentID}

	c.mu.Lock()
	if _, cancelled := c.cancelledSubagents[subagent]; cancelled {
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
	c.nextAssignment++
	pending.SubagentID = subagentID
	pending.AssignmentID = assignmentID
	assignments := &responseAssignment{id: assignmentID, scope: scopeKey, sequence: c.nextAssignment, pending: pending}
	c.assignments[subagent] = append(c.assignments[subagent], assignments)
	scope.subagentCounts[subagentID]++
	scope.pendingResults++
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			queue := c.assignments[subagent]
			for index, candidate := range queue {
				if candidate != assignments {
					continue
				}
				c.assignments[subagent] = append(queue[:index], queue[index+1:]...)
				if len(c.assignments[subagent]) == 0 {
					delete(c.assignments, subagent)
				}
				if registered := c.scopes[scopeKey]; registered != nil && registered.pendingResults > 0 {
					registered.pendingResults--
					registered.subagentCounts[subagentID]--
					if registered.subagentCounts[subagentID] == 0 {
						delete(registered.subagentCounts, subagentID)
					}
				}
				return
			}
		})
	}
}

// CancelSubagentAssignments removes every result obligation that has not yet
// been reserved for delivery for subagentID. Application-owned destructive close
// paths call this after the subagent is durably closed so a result that can no
// longer arrive cannot keep any main agent response scope open forever.
func (c *ResponseScopeCoordinator) CancelSubagentAssignments(sessionID, subagentID string) int {
	if c == nil || sessionID == "" || subagentID == "" {
		return 0
	}
	subagent := responseSubagentKey{sessionID: sessionID, subagentID: subagentID}

	c.mu.Lock()
	c.cancelledSubagents[subagent] = struct{}{}
	queue := c.assignments[subagent]
	if len(queue) == 0 {
		c.mu.Unlock()
		return 0
	}
	delete(c.assignments, subagent)
	scopeIDs := make([]string, 0, len(queue))
	seenScopes := make(map[responseScopeKey]struct{}, len(queue))
	for _, assignments := range queue {
		scope := c.scopes[assignments.scope]
		if scope == nil {
			continue
		}
		if _, seen := seenScopes[assignments.scope]; !seen {
			seenScopes[assignments.scope] = struct{}{}
			scopeIDs = append(scopeIDs, assignments.scope.scopeID)
		}
		if scope.pendingResults > 0 {
			scope.pendingResults--
		}
		if scope.subagentCounts[subagentID] > 0 {
			scope.subagentCounts[subagentID]--
			if scope.subagentCounts[subagentID] == 0 {
				delete(scope.subagentCounts, subagentID)
			}
		}
	}
	logger := c.logger
	c.mu.Unlock()
	sort.Strings(scopeIDs)
	if logger != nil {
		logger.DebugContext(c.ctx, "response scope result obligations cancelled",
			"session_id", sessionID,
			"subagent_id", subagentID,
			"cancelled_assignments", len(queue),
			"scope_ids", scopeIDs,
		)
	}
	return len(queue)
}

// ReserveResultTurn binds a result continuation to the response scope
// that accepted the corresponding subagent assignments.
func (c *ResponseScopeCoordinator) ReserveResultTurn(sessionID, continuationTurnID, subagentID, resultTurnID string) (*ResponseScopeReservation, error) {
	return c.ReserveResultTurnWithMetadata(sessionID, continuationTurnID, ResponseScopeDeliveredResult{
		SubagentID:     subagentID,
		SubagentTurnID: resultTurnID,
	})
}

// ReserveResultTurnWithMetadata binds a result continuation to its
// originating response scope and atomically records the result identity and
// result used by ResultProgress.
func (c *ResponseScopeCoordinator) ReserveResultTurnWithMetadata(sessionID, continuationTurnID string, result ResponseScopeDeliveredResult) (*ResponseScopeReservation, error) {
	subagentID := result.SubagentID
	resultTurnID := result.SubagentTurnID
	if c == nil {
		return nil, errors.New("response scope coordinator is nil")
	}
	if sessionID == "" || continuationTurnID == "" || subagentID == "" || resultTurnID == "" {
		return nil, errors.New("result response scope identifiers are required")
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: continuationTurnID}
	subagent := responseSubagentKey{sessionID: sessionID, subagentID: subagentID}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.turns[turn]; exists {
		return nil, fmt.Errorf("response turn %q already belongs to a scope", continuationTurnID)
	}

	if seen := c.results[subagent]; seen != nil {
		if prior, duplicate := seen[resultTurnID]; duplicate {
			scope := c.scopes[prior.scope]
			if scope == nil {
				return nil, errors.New("result response scope no longer exists")
			}
			scope.activeTurns++
			c.turns[turn] = prior.scope
			c.resultTurns[turn] = struct{}{}
			return &ResponseScopeReservation{
				coordinator: c,
				turn:        turn,
				scope:       prior.scope,
			}, nil
		}
	}

	queue := c.assignments[subagent]
	if len(queue) == 0 {
		return nil, ErrResponseScopeAssignmentNotFound
	}
	assignments := queue[0]
	c.assignments[subagent] = queue[1:]
	if len(c.assignments[subagent]) == 0 {
		delete(c.assignments, subagent)
	}
	scope := c.scopes[assignments.scope]
	if scope == nil || scope.state != responseScopeOpen {
		return nil, errors.New("subagent result response scope is not open")
	}
	if scope.pendingResults == 0 {
		return nil, errors.New("subagent result counter is inconsistent")
	}
	scope.pendingResults--
	scope.activeTurns++
	c.turns[turn] = assignments.scope
	c.resultTurns[turn] = struct{}{}
	if c.results[subagent] == nil {
		c.results[subagent] = make(map[string]resultRecord)
	}
	received := deliveredResultFromAssignment(result, assignments)
	scope.deliveredResults = append(scope.deliveredResults, received)
	c.results[subagent][resultTurnID] = resultRecord{scope: assignments.scope, received: received}
	return &ResponseScopeReservation{
		coordinator: c,
		turn:        turn,
		scope:       assignments.scope,
		assignments: assignments,
	}, nil
}

// ReserveInlineResult binds one result to an already-active turn in the
// same response scope. The result obligation stays non-quiescent as a
// pending runtime input until the active run has durably appended it at a
// provider boundary.
func (c *ResponseScopeCoordinator) ReserveInlineResult(sessionID, activeTurnID, subagentID, resultTurnID string) (*ResponseScopeReservation, error) {
	return c.ReserveInlineResultWithMetadata(sessionID, activeTurnID, ResponseScopeDeliveredResult{
		SubagentID:     subagentID,
		SubagentTurnID: resultTurnID,
	})
}

// ReserveInlineResultWithMetadata binds a result to an active compatible
// turn and atomically records the result identity and result.
func (c *ResponseScopeCoordinator) ReserveInlineResultWithMetadata(sessionID, activeTurnID string, result ResponseScopeDeliveredResult) (*ResponseScopeReservation, error) {
	subagentID := result.SubagentID
	resultTurnID := result.SubagentTurnID
	if c == nil {
		return nil, errors.New("response scope coordinator is nil")
	}
	if sessionID == "" || activeTurnID == "" || subagentID == "" || resultTurnID == "" {
		return nil, errors.New("inline result response scope identifiers are required")
	}
	turn := responseTurnKey{sessionID: sessionID, turnID: activeTurnID}
	subagent := responseSubagentKey{sessionID: sessionID, subagentID: subagentID}

	c.mu.Lock()
	defer c.mu.Unlock()
	activeScope, found := c.turns[turn]
	if !found {
		return nil, errors.New("active result turn does not belong to a response scope")
	}
	if seen := c.results[subagent]; seen != nil {
		if _, duplicate := seen[resultTurnID]; duplicate {
			return nil, errors.New("subagent result was already reserved")
		}
	}
	queue := c.assignments[subagent]
	if len(queue) == 0 {
		return nil, ErrResponseScopeAssignmentNotFound
	}
	assignments := queue[0]
	if assignments.scope != activeScope {
		return nil, errors.New("active turn belongs to a different response scope")
	}
	scope := c.scopes[assignments.scope]
	if scope == nil || scope.state != responseScopeOpen {
		return nil, errors.New("subagent result response scope is not open")
	}
	if scope.pendingResults == 0 {
		return nil, errors.New("subagent result counter is inconsistent")
	}
	c.assignments[subagent] = queue[1:]
	if len(c.assignments[subagent]) == 0 {
		delete(c.assignments, subagent)
	}
	scope.pendingResults--
	scope.pendingInputs++
	if c.results[subagent] == nil {
		c.results[subagent] = make(map[string]resultRecord)
	}
	received := deliveredResultFromAssignment(result, assignments)
	scope.deliveredResults = append(scope.deliveredResults, received)
	c.results[subagent][resultTurnID] = resultRecord{scope: assignments.scope, received: received}
	return &ResponseScopeReservation{
		coordinator: c,
		turn:        turn,
		scope:       assignments.scope,
		assignments: assignments,
		inline:      true,
	}, nil
}

func deliveredResultFromAssignment(result ResponseScopeDeliveredResult, assignments *responseAssignment) *ResponseScopeDeliveredResult {
	received := result
	if assignments != nil {
		received.AssignmentID = assignments.pending.AssignmentID
		if received.DefinitionName == "" {
			received.DefinitionName = assignments.pending.DefinitionName
		}
		if received.DisplayName == "" {
			received.DisplayName = assignments.pending.DisplayName
		}
	}
	return &received
}

// ResultProgress returns the result-accounting snapshot associated with
// this reservation. It includes the result currently being delivered.
func (r *ResponseScopeReservation) ResultProgress() ResponseScopeResultProgress {
	if r == nil || r.coordinator == nil {
		return ResponseScopeResultProgress{
			AllResultsDelivered: true,
			PendingResults:      []ResponseScopePendingResult{},
			DeliveredResults:    []ResponseScopeDeliveredResult{},
		}
	}
	c := r.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resultProgressLocked(r.scope)
}

func (c *ResponseScopeCoordinator) resultProgressLocked(scopeKey responseScopeKey) ResponseScopeResultProgress {
	progress := ResponseScopeResultProgress{
		PendingResults:   []ResponseScopePendingResult{},
		DeliveredResults: []ResponseScopeDeliveredResult{},
	}
	scope := c.scopes[scopeKey]
	if scope == nil {
		progress.AllResultsDelivered = true
		return progress
	}
	type sequencedPending struct {
		sequence uint64
		result   ResponseScopePendingResult
	}
	pending := make([]sequencedPending, 0, scope.pendingResults)
	for _, queue := range c.assignments {
		for _, assignments := range queue {
			if assignments != nil && assignments.scope == scopeKey {
				pending = append(pending, sequencedPending{sequence: assignments.sequence, result: assignments.pending})
			}
		}
	}
	sort.Slice(pending, func(left, right int) bool {
		return pending[left].sequence < pending[right].sequence
	})
	for _, candidate := range pending {
		progress.PendingResults = append(progress.PendingResults, candidate.result)
	}
	for _, received := range scope.deliveredResults {
		if received != nil {
			progress.DeliveredResults = append(progress.DeliveredResults, *received)
		}
	}
	progress.PendingCount = scope.pendingResults
	progress.AllResultsDelivered = scope.pendingResults == 0
	return progress
}

// Commit keeps a result turn reservation after the runtime accepts it.
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

// Rollback restores the result assignments when runtime admission fails.
func (r *ResponseScopeReservation) Rollback(subagentID, resultTurnID string) {
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
		delete(c.resultTurns, r.turn)
		if scope != nil && scope.activeTurns > 0 {
			scope.activeTurns--
		}
	}
	if r.assignments != nil {
		subagent := responseSubagentKey{sessionID: r.turn.sessionID, subagentID: subagentID}
		if _, cancelled := c.cancelledSubagents[subagent]; !cancelled {
			c.assignments[subagent] = append([]*responseAssignment{r.assignments}, c.assignments[subagent]...)
			if scope != nil {
				scope.pendingResults++
			}
		}
		if seen := c.results[subagent]; seen != nil {
			record := seen[resultTurnID]
			if scope != nil && record.received != nil {
				for index, received := range scope.deliveredResults {
					if received == record.received {
						scope.deliveredResults = append(scope.deliveredResults[:index], scope.deliveredResults[index+1:]...)
						break
					}
				}
			}
			delete(seen, resultTurnID)
			if len(seen) == 0 {
				delete(c.results, subagent)
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
		scope.pendingResults == 0 &&
		scope.pendingInputs == 0
}

// EndResponseScopeBoundaryReached allows result continuation turns to
// deliver their final boundary tool on provider step one, while retaining the
// first-action guard for the initial human main-agent turn.
func (c *ResponseScopeCoordinator) EndResponseScopeBoundaryReached(request agentruntime.ToolRequest) bool {
	if request.CompletionBoundary {
		return true
	}
	turn := responseTurnKey{sessionID: request.SessionID, turnID: request.TurnID}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, resultTurn := c.resultTurns[turn]; resultTurn {
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

// ExecuteEndResponseScope runs handler only after the initial human main-agent
// action, or from a result/final repair boundary, when completing the
// current turn would end its response scope. Earlier calls are successful
// no-ops but deliberately do not satisfy the required trigger.
func (c *ResponseScopeCoordinator) ExecuteEndResponseScope(
	ctx context.Context,
	request agentruntime.ToolRequest,
	handler Handler,
) (json.RawMessage, bool, error) {
	if c == nil {
		return nil, false, errors.New("response scope coordinator is not configured")
	}
	turn := responseTurnKey{sessionID: request.SessionID, turnID: request.TurnID}

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
	_, resultTurn := c.resultTurns[turn]
	boundaryReached := request.CompletionBoundary || resultTurn || request.ProviderStep > 1
	ready := boundaryReached &&
		scope.activeTurns == 1 &&
		scope.pendingResults == 0 &&
		scope.pendingInputs == 0
	if !ready {
		logger := c.logger
		activeTurns := scope.activeTurns
		pendingResults := scope.pendingResults
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
				"pending_results", pendingResults,
				"pending_inputs", pendingInputs,
			)
		}
		instruction := "The tool call was handled, but the action did not run because the complete final response is not ready. Treat this call as handled and do not retry it yourself. Finish remaining independent work, or stop if required subagent results are still pending."
		output, err := json.Marshal(map[string]any{
			"status":      "succeeded",
			"action":      "skipped",
			"executed":    false,
			"reason":      "tool_called_at_wrong_time",
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
	var subagentIDs []string
	cleanup := c.cleanup
	logger := c.logger
	if beginEnding {
		scope.state = responseScopeEnding
		subagentIDs = make([]string, 0, len(scope.subagentCounts))
		for subagentID := range scope.subagentCounts {
			subagentIDs = append(subagentIDs, subagentID)
		}
		sort.Strings(subagentIDs)
	}
	c.mu.Unlock()

	if beginEnding {
		c.beginEnding(scopeKey, request.TurnID, subagentIDs, []string{request.Call.Name}, cleanup, logger)
	}

	output, err := handler(ctx, cloneRawJSON(request.Call.Arguments))
	if err == nil && !json.Valid(output) {
		err = errors.New("tool returned invalid JSON")
	}

	c.mu.Lock()
	if current := c.scopes[scopeKey]; current != nil {
		delete(current.endScopeExecuting, request.Call.Name)
		if err == nil {
			current.endScopeCompleted[request.Call.Name] = struct{}{}
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
	if scope.activeTurns != 0 || scope.pendingResults != 0 || scope.pendingInputs != 0 || scope.state == responseScopeEnded {
		c.mu.Unlock()
		return
	}
	beginEnding := scope.state == responseScopeOpen
	if beginEnding {
		scope.state = responseScopeEnding
	}
	subagentIDs := make([]string, 0, len(scope.subagentCounts))
	for subagentID := range scope.subagentCounts {
		subagentIDs = append(subagentIDs, subagentID)
	}
	sort.Strings(subagentIDs)
	cleanup := c.cleanup
	logger := c.logger
	toolNames := append([]string(nil), scope.endScopeOrder...)
	c.mu.Unlock()

	if beginEnding {
		c.beginEnding(scopeKey, turnID, subagentIDs, toolNames, cleanup, logger)
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
			"subagent_ids", subagentIDs,
			"tool_names", toolNames,
		)
	}
	c.publishEvent(ScopeEvent{
		Type: EndScope, SessionID: scopeKey.sessionID, ScopeID: scopeKey.scopeID,
		TriggerTurnID: turnID, SubagentIDs: subagentIDs, ToolNames: toolNames,
	})
}

func (c *ResponseScopeCoordinator) beginEnding(
	scopeKey responseScopeKey,
	turnID string,
	subagentIDs []string,
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
			"subagent_ids", subagentIDs,
			"tool_names", toolNames,
		)
	}
	c.publishEvent(ScopeEvent{
		Type: PreEndScope, SessionID: scopeKey.sessionID, ScopeID: scopeKey.scopeID,
		TriggerTurnID: turnID, SubagentIDs: subagentIDs, ToolNames: toolNames,
	})
	if cleanup != nil {
		c.executeCleanup(cleanup, scopeKey, subagentIDs)
	}
}

func (c *ResponseScopeCoordinator) deleteScopeLocked(scopeKey responseScopeKey) {
	delete(c.scopes, scopeKey)
	for turn, scope := range c.turns {
		if scope == scopeKey {
			delete(c.turns, turn)
			delete(c.resultTurns, turn)
		}
	}
	for subagent, queue := range c.assignments {
		kept := queue[:0]
		for _, assignments := range queue {
			if assignments.scope != scopeKey {
				kept = append(kept, assignments)
			}
		}
		if len(kept) == 0 {
			delete(c.assignments, subagent)
		} else {
			c.assignments[subagent] = kept
		}
	}
	// Result tombstones deliberately outlive their response scope. Without
	// them, a late replay from an older scope could consume the first pending
	// assignments of a newer scope that happens to reuse the same subagent.
}

func (c *ResponseScopeCoordinator) executeCleanup(cleanup ResponseScopeCleanup, scope responseScopeKey, subagentIDs []string) {
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
	cleanup(ctx, scope.sessionID, scope.scopeID, append([]string(nil), subagentIDs...))
}

func containsResponseScopeTool(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}
