package agentruntime

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage"
)

// Config supplies the runtime's provider, transcript store, and explicitly
// owned shared tool transports.
type Config struct {
	Model                   Model
	Messages                storage.MessageStorage
	SystemPrompts           []string
	ContextReminderProvider ContextReminderProvider
	CompletionGuard         CompletionGuard
	InputGuard              InputGuard
	OutputGuard             OutputGuard
	InputGuardPrompt        string
	OutputGuardPrompt       string
	InputGuardModel         Model
	OutputGuardModel        Model
	Tools                   []ToolDefinition
	ToolRequests            chan<- ToolRequest
	ToolResults             <-chan ToolResultEnvelope
	ToolInterrupts          chan<- ToolInterrupt
	PermissionRequests      <-chan permission.Request
	PermissionDecisions     chan<- permission.Decision
	ConfirmationRequests    <-chan confirmation.Request
	ConfirmationDecisions   chan<- confirmation.Decision
	PermissionMode          permission.Mode
	// PermissionModeChanged is invoked while Runtime serializes a live mode
	// transition and before the transition event is published. It lets an
	// embedding atomically update the executor's permission policy.
	PermissionModeChanged func(previous, current permission.Mode) error
	IDGenerator           IDGenerator
	MaxSteps              int
}

// Runtime coordinates active turns for independent sessions. Per-turn event
// processing is deliberately added separately; this type owns only registry
// and shared tool-result routing concerns.
type Runtime struct {
	ctx context.Context

	model                   Model
	messages                storage.MessageStorage
	systemPrompts           []string
	contextReminderProvider ContextReminderProvider
	completionGuard         CompletionGuard
	inputGuard              InputGuard
	outputGuard             OutputGuard
	tools                   []ToolDefinition
	toolRequests            chan<- ToolRequest
	toolResults             <-chan ToolResultEnvelope
	toolInterrupts          chan<- ToolInterrupt
	permissionRequests      <-chan permission.Request
	permissionDecisions     chan<- permission.Decision
	confirmationRequests    <-chan confirmation.Request
	confirmationDecisions   chan<- confirmation.Decision
	idGenerator             IDGenerator
	maxSteps                int
	permissionMode          permission.Mode
	permissionModeChanged   func(previous, current permission.Mode) error

	mu                        sync.RWMutex
	active                    map[string]*Run
	toolResultsClosed         bool
	pendingPermissions        map[permission.ID]*pendingPermission
	permissionDecisionsSeen   map[permission.ID]permission.Decision
	pendingConfirmations      map[confirmation.ID]*pendingConfirmation
	confirmationDecisionsSeen map[confirmation.ID]confirmation.Decision
}

// New validates a complete runtime configuration and immediately starts its
// sole tool-result router. A nil context has the same meaning as a background
// context, matching the public subscription API.
func New(ctx context.Context, config Config) (*Runtime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if isNil(config.Model) {
		return nil, invalidRuntimeConfig("model is required")
	}
	if isNil(config.Messages) {
		return nil, invalidRuntimeConfig("message storage is required")
	}
	if config.ToolRequests == nil {
		return nil, invalidRuntimeConfig("tool requests channel is required")
	}
	if config.ToolResults == nil {
		return nil, invalidRuntimeConfig("tool results channel is required")
	}
	if config.ToolInterrupts == nil {
		return nil, invalidRuntimeConfig("tool interrupts channel is required")
	}
	if cap(config.ToolRequests) == 0 || cap(config.ToolResults) == 0 || cap(config.ToolInterrupts) == 0 {
		return nil, invalidRuntimeConfig("tool channels must be buffered")
	}
	if config.MaxSteps < 0 {
		return nil, invalidRuntimeConfig("maximum steps cannot be negative")
	}
	rawInputGuardPrompt := config.InputGuardPrompt
	rawOutputGuardPrompt := config.OutputGuardPrompt
	config.InputGuardPrompt = strings.TrimSpace(rawInputGuardPrompt)
	config.OutputGuardPrompt = strings.TrimSpace(rawOutputGuardPrompt)
	if rawInputGuardPrompt != "" && config.InputGuardPrompt == "" {
		return nil, invalidRuntimeConfig("input guard prompt is empty")
	}
	if rawOutputGuardPrompt != "" && config.OutputGuardPrompt == "" {
		return nil, invalidRuntimeConfig("output guard prompt is empty")
	}
	if config.InputGuard != nil && config.InputGuardPrompt != "" {
		return nil, invalidRuntimeConfig("input guard and input guard prompt cannot both be configured")
	}
	if config.OutputGuard != nil && config.OutputGuardPrompt != "" {
		return nil, invalidRuntimeConfig("output guard and output guard prompt cannot both be configured")
	}
	if config.InputGuardPrompt != "" {
		guardModel := config.InputGuardModel
		if isNil(guardModel) {
			guardModel = config.Model
		}
		if isNil(guardModel) {
			return nil, invalidRuntimeConfig("input guard prompt requires a model")
		}
		config.InputGuard = newPromptInputGuard(guardModel, config.InputGuardPrompt)
	}
	if config.OutputGuardPrompt != "" {
		guardModel := config.OutputGuardModel
		if isNil(guardModel) {
			guardModel = config.Model
		}
		if isNil(guardModel) {
			return nil, invalidRuntimeConfig("output guard prompt requires a model")
		}
		config.OutputGuard = newPromptOutputGuard(guardModel, config.OutputGuardPrompt)
	}
	if config.PermissionMode == "" {
		config.PermissionMode = permission.Default
	}
	if !permission.IsValidMode(config.PermissionMode) {
		return nil, invalidRuntimeConfig("unknown permission mode %q", config.PermissionMode)
	}
	if config.MaxSteps == 0 {
		config.MaxSteps = 20
	}
	if config.IDGenerator == nil {
		config.IDGenerator = cryptoIDGenerator{}
	}
	if (config.PermissionRequests == nil) != (config.PermissionDecisions == nil) {
		return nil, invalidRuntimeConfig("permission requests and decisions must be configured together")
	}
	if config.PermissionRequests != nil && (cap(config.PermissionRequests) == 0 || cap(config.PermissionDecisions) == 0) {
		return nil, invalidRuntimeConfig("permission channels must be buffered")
	}
	if (config.ConfirmationRequests == nil) != (config.ConfirmationDecisions == nil) {
		return nil, invalidRuntimeConfig("confirmation requests and decisions must be configured together")
	}
	if config.ConfirmationRequests != nil && (cap(config.ConfirmationRequests) == 0 || cap(config.ConfirmationDecisions) == 0) {
		return nil, invalidRuntimeConfig("confirmation channels must be buffered")
	}

	runtime := &Runtime{
		ctx:                       ctx,
		model:                     config.Model,
		messages:                  config.Messages,
		systemPrompts:             append([]string(nil), config.SystemPrompts...),
		contextReminderProvider:   config.ContextReminderProvider,
		completionGuard:           config.CompletionGuard,
		inputGuard:                config.InputGuard,
		outputGuard:               config.OutputGuard,
		tools:                     cloneToolDefinitions(config.Tools),
		toolRequests:              config.ToolRequests,
		toolResults:               config.ToolResults,
		toolInterrupts:            config.ToolInterrupts,
		permissionRequests:        config.PermissionRequests,
		permissionDecisions:       config.PermissionDecisions,
		confirmationRequests:      config.ConfirmationRequests,
		confirmationDecisions:     config.ConfirmationDecisions,
		idGenerator:               config.IDGenerator,
		maxSteps:                  config.MaxSteps,
		permissionMode:            config.PermissionMode,
		permissionModeChanged:     config.PermissionModeChanged,
		active:                    make(map[string]*Run),
		pendingPermissions:        make(map[permission.ID]*pendingPermission),
		permissionDecisionsSeen:   make(map[permission.ID]permission.Decision),
		pendingConfirmations:      make(map[confirmation.ID]*pendingConfirmation),
		confirmationDecisionsSeen: make(map[confirmation.ID]confirmation.Decision),
	}
	go runtime.routeToolResults()
	if config.PermissionRequests != nil {
		go runtime.routePermissionRequests()
	}
	if config.ConfirmationRequests != nil {
		go runtime.routeConfirmationRequests()
	}
	return runtime, nil
}

// PermissionMode returns the mode currently used by new runs and permission
// admission. The initial mode of every RunStarted event is exposed through
// AgentEvent.PermissionMode.Current.
func (r *Runtime) PermissionMode() permission.Mode {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.permissionMode
}

// SetPermissionMode changes this agent-global mode. The transition is
// serialized with Start and is published exactly once to every run that is
// active at the transition. Pending permission prompts deliberately remain
// pending; the new mode applies only to subsequently received tool requests.
func (r *Runtime) SetPermissionMode(mode permission.Mode) error {
	if r == nil {
		return ErrInvalidRequest
	}
	if !permission.IsValidMode(mode) {
		return invalidRuntimeConfig("unknown permission mode %q", mode)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.permissionMode
	if previous == mode {
		return nil
	}
	if r.permissionModeChanged != nil {
		if err := r.permissionModeChanged(previous, mode); err != nil {
			return err
		}
	}
	r.permissionMode = mode
	change := &PermissionModeChange{Previous: previous, Current: mode}
	for _, run := range r.active {
		run.publish(AgentEvent{Type: PermissionModeChanged, PermissionMode: change})
	}
	return nil
}

// ResolvePermission accepts a caller decision and forwards it to the executor.
func (r *Runtime) ResolvePermission(ctx context.Context, decision permission.Decision) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.permissionDecisions == nil {
		return permission.ErrClosed
	}
	if err := permission.ValidateDecision(decision); err != nil {
		return err
	}
	for {
		r.mu.Lock()
		if prior, exists := r.permissionDecisionsSeen[decision.PermissionID]; exists {
			r.mu.Unlock()
			if prior == decision {
				return nil
			}
			return permission.ErrAlreadyResolved
		}
		pending := r.pendingPermissions[decision.PermissionID]
		if pending == nil {
			r.mu.Unlock()
			return permission.ErrNotFound
		}
		request := pending.request
		if request.SessionID != decision.SessionID || request.TurnID != decision.TurnID || request.CallID != decision.CallID {
			r.mu.Unlock()
			return permission.ErrNotFound
		}
		if request.ExpiresAt != nil && !request.ExpiresAt.After(time.Now()) {
			delete(r.pendingPermissions, decision.PermissionID)
			close(pending.done)
			run := pending.run
			r.mu.Unlock()
			run.publish(AgentEvent{Type: AgentPermissionExpired, Permission: &request})
			return permission.ErrClosed
		}
		if pending.forwarding {
			done := pending.done
			r.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		pending.forwarding = true
		r.mu.Unlock()
		err := r.forwardPermissionDecision(ctx, decision)
		r.mu.Lock()
		current := r.pendingPermissions[decision.PermissionID]
		if current == pending {
			pending.forwarding = false
			close(pending.done)
			pending.done = make(chan struct{})
		}
		if err != nil {
			r.mu.Unlock()
			return err
		}
		if current != pending {
			r.mu.Unlock()
			return permission.ErrClosed
		}
		delete(r.pendingPermissions, decision.PermissionID)
		r.permissionDecisionsSeen[decision.PermissionID] = decision
		run := pending.run
		// Keep the registry lock through publication and release any result that
		// raced ahead of this commit only afterwards. A router therefore sees
		// either a pending permission and holds the result, or this committed
		// AgentPermissionResolved event.
		run.publish(AgentEvent{Type: AgentPermissionResolved, Permission: &request, Decision: &decision})
		heldResults := pending.heldToolResults
		r.mu.Unlock()
		for _, result := range heldResults {
			run.enqueueToolResult(result)
		}
		return nil
	}
}

func (r *Runtime) forwardPermissionDecision(ctx context.Context, decision permission.Decision) (err error) {
	defer func() {
		if recover() != nil {
			err = permission.ErrClosed
		}
	}()
	select {
	case r.permissionDecisions <- decision:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.ctx.Done():
		return permission.ErrClosed
	}
}

// Start normalizes the request, rejects previously persisted or active turns,
// registers the new run, then starts its independent serialized coordinator.
func (r *Runtime) Start(ctx context.Context, request Request) (*Run, error) {
	run, _, err := r.start(ctx, request, false)
	return run, err
}

// StartSubscribed creates a run with a live subscription installed before its
// RunStarted event can be committed. The subscription is therefore safe for a
// caller that needs every event from the beginning of processing.
func (r *Runtime) StartSubscribed(ctx context.Context, request Request) (*Run, EventSubscription, error) {
	return r.start(ctx, request, true)
}

func (r *Runtime) start(ctx context.Context, request Request, subscribe bool) (*Run, EventSubscription, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeRequest(request, r.idGenerator)
	if err != nil {
		return nil, EventSubscription{}, err
	}
	if r.inputGuard != nil {
		decision, guardErr := invokeInputGuard(ctx, r.inputGuard, InputGuardAttempt{
			SessionID: normalized.SessionID,
			TurnID:    normalized.TurnID,
			Message:   storage.CloneMessage(normalized.Message),
		})
		if guardErr != nil {
			return nil, EventSubscription{}, fmt.Errorf("evaluate input guard: %w", guardErr)
		}
		if err := validateInputGuardDecision(decision, normalized.SessionID, normalized.TurnID); err != nil {
			return nil, EventSubscription{}, fmt.Errorf("evaluate input guard: %w", err)
		}
		switch decision.Action {
		case InputReject:
			return nil, EventSubscription{}, fmt.Errorf("%w: %s", ErrInputGuardRejected, decision.Reason)
		case InputReplace:
			replacement := storage.CloneMessage(*decision.Message)
			if replacement.Type != normalized.Message.Type {
				return nil, EventSubscription{}, fmt.Errorf("evaluate input guard replacement: message type changes from %q to %q", normalized.Message.Type, replacement.Type)
			}
			replacement.SessionID = normalized.SessionID
			replacement.TurnID = normalized.TurnID
			replacement.ID = normalized.Message.ID
			replacement.CreatedAt = normalized.Message.CreatedAt
			if err := storage.ValidateMessage(replacement); err != nil {
				return nil, EventSubscription{}, fmt.Errorf("evaluate input guard replacement: %w", err)
			}
			normalized.Message = replacement
		}
	}
	exists, err := r.messages.TurnExists(ctx, normalized.SessionID, normalized.TurnID)
	if err != nil {
		return nil, EventSubscription{}, fmt.Errorf("check existing turn: %w", err)
	}
	if exists {
		return nil, EventSubscription{}, ErrTurnExists
	}

	run := newRun(normalized.SessionID, normalized.TurnID)
	r.mu.Lock()
	for {
		if r.toolResultsClosed {
			r.mu.Unlock()
			return nil, EventSubscription{}, ErrToolResultsClosed
		}
		active := r.active[normalized.SessionID]
		if active == nil {
			break
		}
		if !active.Done() {
			r.mu.Unlock()
			return nil, EventSubscription{}, ErrTurnInProgress
		}
		// A terminal event is visible before its final persistence and registry
		// cleanup effects finish. Wait for that small lifecycle boundary so a
		// caller can safely start the next turn as soon as its subscription
		// closes without racing ErrTurnInProgress or transcript ordering.
		finished := active.finished
		r.mu.Unlock()
		select {
		case <-finished:
		case <-ctx.Done():
			return nil, EventSubscription{}, ctx.Err()
		case <-r.ctx.Done():
			return nil, EventSubscription{}, context.Cause(r.ctx)
		}
		r.mu.Lock()
	}
	var eventSubscription EventSubscription
	if subscribe {
		eventSubscription = run.Subscribe(ctx)
	}
	r.active[normalized.SessionID] = run
	started := make(chan struct{})
	go run.runLoop(ctx, r, normalized.Message, r.permissionMode, started)
	// Keep the registry lock until RunStarted is committed. This gives every
	// run a stable initial-mode event before a concurrent mode transition can
	// publish to it.
	<-started
	r.mu.Unlock()
	return run, eventSubscription, nil
}

// Interrupt requests interruption of the exact active session and turn. The
// active-map lock keeps a completing run registered while its control request
// is accepted, and the Run itself makes repeat requests idempotent.
func (r *Runtime) Interrupt(ctx context.Context, sessionID, turnID, reason string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	run := r.active[sessionID]
	if run == nil || run.TurnID() != turnID {
		r.mu.RUnlock()
		return ErrRunNotFound
	}
	err := run.Interrupt(ctx, reason)
	r.mu.RUnlock()
	return err
}

// unregister removes run only when it still owns its session entry. This
// compare-before-delete rule lets a terminal older run never remove a newer
// run for the same session.
func (r *Runtime) unregister(run *Run) {
	if run == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[run.SessionID()] == run {
		delete(r.active, run.SessionID())
	}
	for id, pending := range r.pendingPermissions {
		if pending.run == run {
			delete(r.pendingPermissions, id)
			if pending.forwarding {
				close(pending.done)
			}
		}
	}
	for id, decision := range r.permissionDecisionsSeen {
		if decision.SessionID == run.SessionID() && decision.TurnID == run.TurnID() {
			delete(r.permissionDecisionsSeen, id)
		}
	}
	for id, pending := range r.pendingConfirmations {
		if pending.run == run {
			delete(r.pendingConfirmations, id)
			if pending.forwarding {
				close(pending.done)
			}
		}
	}
	for id, decision := range r.confirmationDecisionsSeen {
		if decision.SessionID == run.SessionID() && decision.TurnID == run.TurnID() {
			delete(r.confirmationDecisionsSeen, id)
		}
	}
}

func invalidRuntimeConfig(format string, arguments ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidRequest}, arguments...)...)
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	typeOf := reflect.ValueOf(value)
	switch typeOf.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return typeOf.IsNil()
	default:
		return false
	}
}
