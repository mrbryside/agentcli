package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"harness-api/permission"
	"harness-api/provider"
	"harness-api/storage"
)

// Run owns the in-memory history and subscriptions for one agent turn. Its
// coordinator is added separately; this type deliberately keeps publication
// and subscriber delivery independent of provider and tool work.
type Run struct {
	mu        sync.RWMutex
	sessionID string
	turnID    string
	state     AgentState

	subscribers      map[uint64]*runSubscription
	nextSubscriberID uint64

	done        bool
	result      RunResult
	resultSet   bool
	terminalErr error
	// interruptRequested makes the public control operation idempotent while
	// the run is still registered and waiting for its coordinator to consume
	// the one control message.
	interruptRequested bool

	// The following coordinator-owned values are initialized here so routing,
	// interruption, and provider execution can share this Run without adding
	// another mutable owner of its state.
	control           chan runControl
	toolResults       []ToolResultEnvelope
	toolResultsNotify chan struct{}
	providerCancel    context.CancelFunc
	providerEvents    <-chan provider.StreamEvent
	steps             int
	terminalNotify    chan struct{}
	finished          chan struct{}
	finishOnce        sync.Once
}

type RunStatus string

const (
	RunStatusActive                 RunStatus = "active"
	RunStatusWaitingForPermission   RunStatus = "waiting_for_permission"
	RunStatusWaitingForConfirmation RunStatus = "waiting_for_confirmation"
	RunStatusDone                   RunStatus = "done"
)

func (r *Run) Status() RunStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.done {
		return RunStatusDone
	}
	pending := 0
	for _, e := range r.state.events {
		if e.Type == AgentPermissionRequested {
			pending++
		}
		if e.Type == AgentPermissionResolved || e.Type == AgentPermissionCancelled || e.Type == AgentPermissionExpired {
			if pending > 0 {
				pending--
			}
		}
	}
	if pending > 0 {
		return RunStatusWaitingForPermission
	}
	pendingConfirmations := 0
	for _, event := range r.state.events {
		if event.Type == AgentConfirmationRequested {
			pendingConfirmations++
		}
		if event.Type == AgentConfirmationResolved || event.Type == AgentConfirmationCancelled || event.Type == AgentConfirmationExpired {
			if pendingConfirmations > 0 {
				pendingConfirmations--
			}
		}
	}
	if pendingConfirmations > 0 {
		return RunStatusWaitingForConfirmation
	}
	return RunStatusActive
}

type runSubscription struct {
	channel chan AgentEvent
	notify  chan struct{}
	queue   []AgentEvent
	closed  bool
}

type runControl struct {
	reason string
}

func newRun(sessionID, turnID string) *Run {
	return &Run{
		sessionID:         sessionID,
		turnID:            turnID,
		state:             EmptyState(),
		subscribers:       make(map[uint64]*runSubscription),
		control:           make(chan runControl, 1),
		toolResultsNotify: make(chan struct{}, 1),
		terminalNotify:    make(chan struct{}, 1),
		finished:          make(chan struct{}),
	}
}

// SessionID returns the identity of the conversation containing this turn.
func (r *Run) SessionID() string {
	return r.sessionID
}

// TurnID returns the identity of this run.
func (r *Run) TurnID() string {
	return r.turnID
}

// Subscribe atomically fences retained history and begins a live-only event
// stream. Each subscriber owns an unbounded private queue, so a slow reader
// never delays publication or another subscriber. Events retained through the
// returned Cursor are intentionally not replayed; retrieve them with
// EventsBetween when needed.
func (r *Run) Subscribe(ctx context.Context) EventSubscription {
	if ctx == nil {
		ctx = context.Background()
	}

	subscription := &runSubscription{
		channel: make(chan AgentEvent, 16),
		notify:  make(chan struct{}, 1),
	}

	r.mu.Lock()
	id := r.nextSubscriberID
	r.nextSubscriberID++
	subscription.closed = r.done
	r.subscribers[id] = subscription
	cursor := r.tailCursorLocked()
	r.mu.Unlock()

	go r.runSubscription(ctx, id, subscription)
	return EventSubscription{Cursor: cursor, Events: subscription.channel}
}

func (r *Run) runSubscription(ctx context.Context, id uint64, subscription *runSubscription) {
	for {
		var (
			event    AgentEvent
			hasEvent bool
			closed   bool
		)

		r.mu.Lock()
		if len(subscription.queue) > 0 {
			event = subscription.queue[0]
			subscription.queue = subscription.queue[1:]
			hasEvent = true
		} else if subscription.closed {
			closed = true
			delete(r.subscribers, id)
		}
		r.mu.Unlock()

		if hasEvent {
			select {
			case subscription.channel <- event:
			case <-ctx.Done():
				r.removeSubscriber(id)
				close(subscription.channel)
				return
			}
			continue
		}
		if closed {
			close(subscription.channel)
			return
		}

		select {
		case <-subscription.notify:
		case <-ctx.Done():
			r.removeSubscriber(id)
			close(subscription.channel)
			return
		}
	}
}

func (r *Run) removeSubscriber(id uint64) {
	r.mu.Lock()
	delete(r.subscribers, id)
	r.mu.Unlock()
}

// Events returns an independent snapshot of the event history.
func (r *Run) Events() []AgentEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Events(r.state)
}

// EventsBetween returns independent copies of retained events in (after,
// through]. The cursors must describe this Run. A zero-value after cursor is
// the position before this Run's first event; a zero-value through cursor is
// not a valid fence because it has no run identity.
func (r *Run) EventsBetween(after, through EventCursor) ([]AgentEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.validAfterCursorLocked(after) {
		return nil, fmt.Errorf("%w: after cursor does not identify run %q/%q", ErrInvalidRequest, r.sessionID, r.turnID)
	}
	if through.SessionID != r.sessionID || through.TurnID != r.turnID {
		return nil, fmt.Errorf("%w: through cursor does not identify run %q/%q", ErrInvalidRequest, r.sessionID, r.turnID)
	}
	tail := uint64(len(r.state.events))
	if after.Sequence > tail || through.Sequence > tail {
		return nil, fmt.Errorf("%w: cursor is outside retained event history", ErrInvalidRequest)
	}
	if after.Sequence > through.Sequence {
		return nil, fmt.Errorf("%w: after cursor follows through cursor", ErrInvalidRequest)
	}
	if after.Sequence == through.Sequence {
		return []AgentEvent{}, nil
	}
	events := r.state.events[after.Sequence:through.Sequence]
	clones := make([]AgentEvent, len(events))
	for index, event := range events {
		clones[index] = cloneEvent(event)
	}
	return clones, nil
}

func (r *Run) tailCursorLocked() EventCursor {
	return EventCursor{SessionID: r.sessionID, TurnID: r.turnID, Sequence: uint64(len(r.state.events))}
}

func (r *Run) validAfterCursorLocked(cursor EventCursor) bool {
	if cursor == (EventCursor{}) {
		return true
	}
	return cursor.SessionID == r.sessionID && cursor.TurnID == r.turnID
}

// Done reports whether the run has accepted its one terminal event.
func (r *Run) Done() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.done
}

// Result returns the terminal aggregate cached when the terminal event was
// published. It is unavailable while the run remains active.
func (r *Run) Result() (RunResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.done {
		return RunResult{}, ErrRunNotDone
	}
	if r.terminalErr != nil {
		return RunResult{}, r.terminalErr
	}
	if !r.resultSet {
		return RunResult{}, errors.New("run completed without a result")
	}
	return cloneRunResult(r.result), nil
}

// Interrupt asks this run's coordinator to end the turn. The request is
// delivered through the private control mailbox so the coordinator remains
// the only owner of event ordering and side effects. Repeated requests before
// terminal cleanup are intentionally successful no-ops.
func (r *Run) Interrupt(ctx context.Context, reason string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return ErrRunNotFound
	}
	if r.interruptRequested {
		r.mu.Unlock()
		return nil
	}
	r.interruptRequested = true
	r.mu.Unlock()

	select {
	case r.control <- runControl{reason: reason}:
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		if !r.done {
			r.interruptRequested = false
		}
		r.mu.Unlock()
		return ctx.Err()
	}
}

// publish serializes one state transition. It is intentionally non-blocking
// with respect to subscribers: delivery happens in their own goroutines.
func (r *Run) publish(event AgentEvent) {
	r.transition(event)
}

// transition accepts exactly one event and returns the state that preceded it.
// The event loop uses the returned state as Effects' input; direct publication
// remains available to the router and the subscription tests.
func (r *Run) transition(event AgentEvent) (AgentState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return AgentState{}, false
	}

	previous := r.state
	event = cloneEvent(event)
	event.Sequence = uint64(len(r.state.events) + 1)
	event.SessionID = r.sessionID
	event.TurnID = r.turnID
	r.state = State(r.state, event)
	r.enqueueEventLocked(event)

	if !isTerminalEvent(event.Type) {
		return previous, true
	}

	r.done = true
	switch event.Type {
	case RunCompleted:
		result, err := Result(Events(r.state))
		if err != nil {
			r.terminalErr = err
		} else {
			r.result = cloneRunResult(result)
			r.resultSet = true
		}
	case RunFailed:
		r.terminalErr = event.Error
		if r.terminalErr == nil {
			r.terminalErr = errors.New("run failed")
		}
	case AgentInterrupted:
		r.terminalErr = ErrRunInterrupted
	}
	r.closeSubscribersLocked()
	signalRun(r.terminalNotify)
	return previous, true
}

func isTerminalEvent(eventType EventType) bool {
	return eventType == RunCompleted || eventType == RunFailed || eventType == AgentInterrupted
}

func (r *Run) enqueueEventLocked(event AgentEvent) {
	for _, subscription := range r.subscribers {
		subscription.queue = append(subscription.queue, cloneEvent(event))
		signalRun(subscription.notify)
	}
}

func (r *Run) closeSubscribersLocked() {
	for _, subscription := range r.subscribers {
		subscription.closed = true
		signalRun(subscription.notify)
	}
}

// enqueueToolResult is used by the shared result router. The unbounded queue
// and size-one notification keep result routing independent of the run loop.
func (r *Run) enqueueToolResult(result ToolResultEnvelope) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return false
	}
	r.toolResults = append(r.toolResults, cloneToolResultEnvelope(result))
	signalRun(r.toolResultsNotify)
	return true
}

func (r *Run) drainToolResults() []ToolResultEnvelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	results := r.toolResults
	r.toolResults = nil
	// Clear a stale coalesced notification while the queue lock is held. A
	// router append after this point must wait for the lock and will publish a
	// fresh notification, so no queued result can be left unsignalled.
	select {
	case <-r.toolResultsNotify:
	default:
	}
	if results == nil {
		return nil
	}
	clones := make([]ToolResultEnvelope, len(results))
	for index, result := range results {
		clones[index] = cloneToolResultEnvelope(result)
	}
	return clones
}

func signalRun(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

// runLoop is the sole coordinator for a turn. It serializes state transitions
// and interprets every pure effect in order, while the router and provider
// subscriptions only append to their own mailboxes/channels.
func (r *Run) runLoop(ctx context.Context, runtime *Runtime, initial Message, initialMode permission.Mode, started chan<- struct{}) {
	if ctx == nil {
		ctx = context.Background()
	}
	startedOnce := sync.Once{}
	signalStarted := func() { startedOnce.Do(func() { close(started) }) }
	event := AgentEvent{Type: RunStarted, Message: &initial, PermissionMode: &PermissionModeChange{Current: initialMode}}
	previous, accepted := r.transition(event)
	signalStarted()
	if !accepted || !r.processCommittedEvent(ctx, runtime, event, previous) {
		return
	}

	for {
		if r.Done() {
			r.finish(runtime)
			return
		}

		select {
		case <-runtime.ctx.Done():
			r.processEvent(ctx, runtime, AgentEvent{Type: AgentInterrupted, Reason: contextReason(runtime.ctx)})
			return
		case <-ctx.Done():
			r.processEvent(ctx, runtime, AgentEvent{Type: AgentInterrupted, Reason: contextReason(ctx)})
			return
		case <-r.terminalNotify:
			r.finish(runtime)
			return
		case control := <-r.control:
			r.processEvent(ctx, runtime, AgentEvent{Type: AgentInterrupted, Reason: control.reason})
			return
		case <-r.toolResultsNotify:
			for _, result := range r.drainToolResults() {
				if !r.processEvent(ctx, runtime, AgentEvent{Type: ToolResultReceived, ToolResult: &result}) {
					return
				}
			}
		case event, open := <-r.takeProviderEvents():
			if !open {
				r.setProviderEvents(nil)
				continue
			}
			if !r.processEvent(ctx, runtime, AgentEvent{Type: ProviderEventReceived, ProviderEvent: event}) {
				return
			}
		}
	}
}

// processEvent commits an input event, derives effects from the preceding
// state, and interprets them before accepting another input event.
func (r *Run) processEvent(ctx context.Context, runtime *Runtime, event AgentEvent) bool {
	if event.Type == AgentInterrupted {
		runtime.cancelRunPermissions(r)
		runtime.cancelRunConfirmations(r)
	}
	event.SessionID = r.sessionID
	event.TurnID = r.turnID
	previous, accepted := r.transition(event)
	if !accepted {
		return false
	}
	return r.processCommittedEvent(ctx, runtime, event, previous)
}

func (r *Run) processCommittedEvent(ctx context.Context, runtime *Runtime, event AgentEvent, previous AgentState) bool {

	effects, err := Effects(previous, event)
	if err != nil {
		return r.fail(ctx, runtime, fmt.Errorf("derive effects: %w", err))
	}
	// An interrupted turn still has to persist its synthetic tool results. A
	// Start context is intentionally allowed to stop the provider, but it must
	// not make this terminal cleanup append fail before it reaches storage.
	effectCtx := ctx
	if event.Type == AgentInterrupted {
		effectCtx = context.WithoutCancel(ctx)
	}
	for _, effect := range effects {
		if r.Done() && !isTerminalEvent(event.Type) {
			r.finish(runtime)
			return false
		}
		if !r.interpretEffect(effectCtx, runtime, effect) {
			return false
		}
	}
	return !r.Done()
}

func (r *Run) interpretEffect(ctx context.Context, runtime *Runtime, effect Effect) bool {
	switch effect.Type {
	case EmitEvent:
		if effect.Event == nil {
			return r.fail(ctx, runtime, errors.New("emit event effect without an event"))
		}
		return r.processEvent(ctx, runtime, *effect.Event)
	case AppendMessages:
		if err := r.appendMessages(ctx, runtime, effect.Messages); err != nil {
			return r.fail(ctx, runtime, err)
		}
	case StartProvider:
		if err := r.startProvider(ctx, runtime); err != nil {
			return r.fail(ctx, runtime, err)
		}
	case DispatchTool:
		if effect.ToolRequest == nil {
			return r.fail(ctx, runtime, errors.New("dispatch tool effect without a request"))
		}
		request := cloneToolRequest(*effect.ToolRequest)
		select {
		case runtime.toolRequests <- request:
		case <-ctx.Done():
			return r.processEvent(ctx, runtime, AgentEvent{Type: AgentInterrupted, Reason: contextReason(ctx)})
		case <-runtime.ctx.Done():
			return r.processEvent(ctx, runtime, AgentEvent{Type: AgentInterrupted, Reason: contextReason(runtime.ctx)})
		}
	case InterruptTools:
		if effect.ToolInterrupt == nil {
			return r.fail(ctx, runtime, errors.New("interrupt tools effect without an interrupt"))
		}
		interrupt := cloneToolInterrupt(*effect.ToolInterrupt)
		select {
		case runtime.toolInterrupts <- interrupt:
		case <-runtime.ctx.Done():
			return false
		}
	case CancelProvider:
		r.cancelProvider()
	case CompleteRun, FailRun:
		// publish caches the terminal outcome before effect interpretation.
	case CloseRun:
		r.finish(runtime)
		return false
	default:
		return r.fail(ctx, runtime, fmt.Errorf("unknown effect type %q", effect.Type))
	}
	return true
}

func (r *Run) appendMessages(ctx context.Context, runtime *Runtime, messages []Message) error {
	prepared := storage.CloneMessages(messages)
	for index := range prepared {
		message := &prepared[index]
		if message.SessionID == "" {
			message.SessionID = r.sessionID
		}
		if message.TurnID == "" {
			message.TurnID = r.turnID
		}
		if message.SessionID != r.sessionID || message.TurnID != r.turnID {
			return fmt.Errorf("append message %d has identifiers outside the run", index)
		}
		if message.ID == "" {
			id, err := runtime.idGenerator.NewID("msg_")
			if err != nil {
				return fmt.Errorf("generate message ID: %w", err)
			}
			if id == "" {
				return errors.New("generate message ID: generated ID is empty")
			}
			message.ID = id
		}
		if message.CreatedAt.IsZero() {
			message.CreatedAt = time.Now().UTC()
		} else {
			message.CreatedAt = message.CreatedAt.UTC()
		}
	}
	if err := runtime.messages.Append(ctx, prepared...); err != nil {
		return fmt.Errorf("append messages: %w", err)
	}
	return nil
}

func (r *Run) startProvider(ctx context.Context, runtime *Runtime) error {
	steps := r.providerSteps()
	if steps >= runtime.maxSteps {
		return ErrMaxSteps
	}
	messages, err := runtime.messages.List(ctx, r.sessionID)
	if err != nil {
		return fmt.Errorf("list messages: %w", err)
	}
	var reminders []ContextReminder
	if runtime.contextReminderProvider != nil {
		reminders, err = runtime.contextReminderProvider(ctx, ContextReminderRequest{
			SessionID: r.sessionID,
			TurnID:    r.turnID,
		})
		if err != nil {
			return fmt.Errorf("resolve context reminders: %w", err)
		}
	}

	r.cancelProvider()
	providerCtx, cancel := context.WithCancel(ctx)
	stream, err := runtime.model.Start(providerCtx, ModelRequest{
		SessionID:        r.sessionID,
		TurnID:           r.turnID,
		SystemPrompts:    append([]string(nil), runtime.systemPrompts...),
		ContextReminders: cloneContextReminders(reminders),
		Messages:         storage.CloneMessages(messages),
		Tools:            cloneToolDefinitions(runtime.tools),
	})
	if err != nil {
		cancel()
		return fmt.Errorf("start provider: %w", err)
	}
	if stream == nil {
		cancel()
		return errors.New("start provider: model returned a nil stream")
	}
	r.mu.Lock()
	r.providerCancel = cancel
	r.mu.Unlock()
	r.incrementProviderSteps()
	// The loop reads the current provider subscription through this mailbox-free
	// accessor; startProvider is always called by that one loop goroutine.
	r.setProviderEvents(stream.Subscribe(providerCtx))
	return nil
}

func cloneContextReminders(reminders []ContextReminder) []ContextReminder {
	if reminders == nil {
		return nil
	}
	cloned := make([]ContextReminder, len(reminders))
	copy(cloned, reminders)
	return cloned
}

// provider state belongs to the loop except the cancel function, which can be
// reached by terminal cleanup. Keeping the stream channel in the Run avoids a
// second goroutine or a forwarding queue.
func (r *Run) setProviderEvents(events <-chan provider.StreamEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerEvents = events
}

func (r *Run) takeProviderEvents() <-chan provider.StreamEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providerEvents
}

func (r *Run) providerSteps() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.steps
}

func (r *Run) incrementProviderSteps() {
	r.mu.Lock()
	r.steps++
	r.mu.Unlock()
}

func (r *Run) cancelProvider() {
	r.mu.Lock()
	cancel := r.providerCancel
	r.providerCancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Run) fail(ctx context.Context, runtime *Runtime, err error) bool {
	if err == nil {
		err = errors.New("agent run failed")
	}
	return r.processEvent(ctx, runtime, AgentEvent{Type: RunFailed, Error: err})
}

func (r *Run) finish(runtime *Runtime) {
	r.cancelProvider()
	runtime.unregister(r)
	r.finishOnce.Do(func() { close(r.finished) })
}

func contextReason(ctx context.Context) string {
	if err := context.Cause(ctx); err != nil {
		return err.Error()
	}
	return ErrRunInterrupted.Error()
}
