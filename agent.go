// Package agentcli assembles the runtime and tool executor needed by an agent
// user interface. It intentionally keeps transport channels private while
// leaving Runtime available for advanced per-run controls and subscriptions.
package agentcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
	"github.com/mrbryside/agentcli/toolexecution"
)

// Agent owns one runtime and its private tool executor.
type Agent struct {
	runtime       *agentruntime.Runtime
	messages      storage.MessageStorage
	project       *Project
	context       context.Context
	cancel        context.CancelFunc
	closing       context.Context
	closingCancel context.CancelFunc

	operationMu sync.RWMutex

	executorDone chan struct{}
	executorMu   sync.RWMutex
	executorErr  error

	closeOnce sync.Once
	closeErr  error

	subagents *subagentManager
}

// New creates an agent with in-memory storage and no tools by default.
// Its tool executor starts before New returns, so Start may be called
// immediately.
func New(ctx context.Context, options ...Option) (*Agent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	projectRoot, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve default project root: %w", err)
	}
	configuration := defaultConfig(projectRoot)
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("agentcli option %d is nil", index)
		}
		if err := option(&configuration); err != nil {
			return nil, fmt.Errorf("agentcli option %d: %w", index, err)
		}
	}
	if configuration.project != nil && !configuration.childAgent {
		if err := validateProjectToolAllowlists(configuration.project, configuration.tools); err != nil {
			return nil, err
		}
	}
	// Projects without definitions deliberately allocate no child-session
	// resources. A child itself is also not a parent manager, keeping the
	// initial nesting depth at one.
	if configuration.project != nil && len(configuration.project.subagents) != 0 && !configuration.childAgent {
		if configuration.subagents == nil {
			configuration.subagents = inmemory.NewSubagentStorage()
		}
		if configuration.maxSubagents == 0 {
			configuration.maxSubagents = defaultMaxSubagents
		}
	}
	if err := configuration.validate(); err != nil {
		return nil, err
	}
	policyController, err := toolexecution.NewPermissionController(configuration.permissionPolicy)
	if err != nil {
		return nil, fmt.Errorf("create permission controller: %w", err)
	}

	registry := toolexecution.NewRegistry()
	var subagentTools *toolexecution.SubagentToolBridge
	rootHasSubagents := configuration.project != nil && len(configuration.project.subagents) != 0 && !configuration.childAgent
	if rootHasSubagents {
		for _, tool := range configuration.tools {
			if toolexecution.IsSubagentToolName(tool.Definition.Name) || tool.Definition.Name == toolexecution.SubagentOutcomeToolName {
				return nil, fmt.Errorf("custom tool %q conflicts with reserved subagent tool", tool.Definition.Name)
			}
		}
		subagentTools = toolexecution.NewSubagentToolBridge()
	}
	// Keep configuration.tools as caller-provided tools only. The skill loader
	// is a per-Agent framework tool; retaining it in config would make child
	// construction inherit it and register a duplicate loader of its own.
	runtimeTools := configuration.tools
	if configuration.project != nil && !configuration.childAgent {
		runtimeTools = configuration.project.filterRootTools(configuration.tools)
	}
	registeredTools := append([]toolexecution.Tool(nil), runtimeTools...)
	if configuration.project != nil && len(configuration.project.skills) != 0 {
		registeredTools = append(registeredTools, toolexecution.NewSkillLoader(
			configuration.project.executionSkills(),
			configuration.messages,
			configuration.skillReload,
		).Tool())
	}
	if configuration.childAgent {
		registeredTools = append(registeredTools, toolexecution.NewSubagentOutcomeTool())
	}
	for _, tool := range registeredTools {
		if err := registry.Register(tool); err != nil {
			return nil, fmt.Errorf("register tool: %w", err)
		}
	}
	if subagentTools != nil {
		for _, tool := range subagentTools.Tools() {
			if err := registry.Register(tool); err != nil {
				return nil, fmt.Errorf("register subagent tool: %w", err)
			}
		}
	}
	runContext, cancel := context.WithCancel(ctx)
	toolRequests := make(chan agentruntime.ToolRequest, configuration.channelBuffer)
	toolResults := make(chan agentruntime.ToolResultEnvelope, configuration.channelBuffer)
	toolInterrupts := make(chan agentruntime.ToolInterrupt, configuration.channelBuffer)
	permissionRequests := make(chan permission.Request, configuration.channelBuffer)
	permissionDecisions := make(chan permission.Decision, configuration.channelBuffer)
	confirmationRequests := make(chan confirmation.Request, configuration.channelBuffer)
	confirmationDecisions := make(chan confirmation.Decision, configuration.channelBuffer)

	closing, closeSignal := context.WithCancel(context.Background())
	agent := &Agent{messages: configuration.messages, project: configuration.project, context: runContext, cancel: cancel, closing: closing, closingCancel: closeSignal, executorDone: make(chan struct{})}
	var manager *subagentManager
	if rootHasSubagents {
		manager, err = newSubagentManager(agent, configuration)
		if err != nil {
			closeSignal()
			cancel()
			return nil, fmt.Errorf("create subagent manager: %w", err)
		}
		subagentTools.Bind(manager)
	}
	reminderProvider := configuration.contextReminderProvider
	var completionGuard agentruntime.CompletionGuard
	if configuration.childAgent {
		completionGuard = subagentOutcomeCompletionGuard
	} else {
		completionGuard = callbackDeliveryCompletionGuard
	}
	if manager != nil {
		reminderProvider = composeContextReminderProviders(reminderProvider, subagentReminderProvider(manager))
	}

	runtime, err := agentruntime.New(runContext, agentruntime.Config{
		Model:                   configuration.model,
		Messages:                configuration.messages,
		SystemPrompts:           append([]string(nil), configuration.systemPrompts...),
		ContextReminderProvider: reminderProvider,
		CompletionGuard:         completionGuard,
		Tools:                   registry.Definitions(),
		ToolRequests:            toolRequests,
		ToolResults:             toolResults,
		ToolInterrupts:          toolInterrupts,
		PermissionRequests:      permissionRequests,
		PermissionDecisions:     permissionDecisions,
		ConfirmationRequests:    confirmationRequests,
		ConfirmationDecisions:   confirmationDecisions,
		PermissionMode:          configuration.permissionMode,
		PermissionModeChanged: func(_, current permission.Mode) error {
			return policyController.SetMode(current)
		},
	})
	if err != nil {
		closeSignal()
		cancel()
		return nil, fmt.Errorf("create runtime: %w", err)
	}
	executor, err := toolexecution.NewExecutor(registry, configuration.toolWorkers, toolexecution.Config{
		PermissionEnabled:     true,
		NonInteractive:        configuration.nonInteractive,
		PermissionRequests:    permissionRequests,
		PermissionDecisions:   permissionDecisions,
		Policy:                configuration.permissionPolicy,
		PermissionController:  policyController,
		ProjectID:             configuration.projectRoot,
		Store:                 configuration.permissions,
		ConfirmationEnabled:   true,
		ConfirmationRequests:  confirmationRequests,
		ConfirmationDecisions: confirmationDecisions,
		ConfirmationStore:     configuration.confirmations,
	})
	if err != nil {
		closeSignal()
		cancel()
		return nil, fmt.Errorf("create tool executor: %w", err)
	}

	agent.runtime = runtime
	agent.subagents = manager
	go func() {
		err := executor.Run(runContext, toolRequests, toolResults, toolInterrupts)
		agent.executorMu.Lock()
		agent.executorErr = err
		agent.executorMu.Unlock()
		close(agent.executorDone)
	}()
	return agent, nil
}

func validateProjectToolAllowlists(project *Project, tools []toolexecution.Tool) error {
	registered := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		registered[tool.Definition.Name] = struct{}{}
	}
	if project.restrictTools {
		for _, name := range project.toolNames {
			if _, found := registered[name]; !found {
				return fmt.Errorf("root agent requires custom tool %q, but it is not registered", name)
			}
		}
	}
	for _, definition := range project.Subagents() {
		for _, name := range definition.Tools {
			if _, found := registered[name]; !found {
				return fmt.Errorf("subagent %q requires custom tool %q, but it is not registered", definition.Name, name)
			}
		}
	}
	return nil
}

func (project *Project) filterRootTools(tools []toolexecution.Tool) []toolexecution.Tool {
	if project == nil || !project.restrictTools {
		return append([]toolexecution.Tool(nil), tools...)
	}
	allowed := make(map[string]struct{}, len(project.toolNames))
	for _, name := range project.toolNames {
		allowed[name] = struct{}{}
	}
	filtered := make([]toolexecution.Tool, 0, len(allowed))
	for _, tool := range tools {
		if _, found := allowed[tool.Definition.Name]; found {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// SetPermissionMode atomically changes the mode used by runtime and executor
// admission. Pending prompts remain pending; only new
// tool requests use the new mode. Active runs receive PermissionModeChanged.
func (a *Agent) SetPermissionMode(ctx context.Context, mode permission.Mode) error {
	if a == nil || a.runtime == nil {
		return errors.New("agent is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !isPermissionMode(mode) {
		return fmt.Errorf("unknown permission mode %q", mode)
	}
	a.operationMu.Lock()
	defer a.operationMu.Unlock()
	if a.isClosing() {
		return ErrClosed
	}
	err := a.runtime.SetPermissionMode(mode)
	if err == nil && a.subagents != nil {
		err = a.subagents.setPermissionMode(ctx, mode)
	}
	if a.isClosing() {
		return ErrClosed
	}
	return err
}

// PermissionMode returns the current agent-global permission mode.
func (a *Agent) PermissionMode() permission.Mode {
	if a == nil || a.runtime == nil {
		return ""
	}
	return a.runtime.PermissionMode()
}

// Start begins one run. For a subscription that cannot miss RunStarted, use
// StartSubscribed.
func (a *Agent) Start(ctx context.Context, request agentruntime.Request) (*agentruntime.Run, error) {
	if a == nil || a.runtime == nil {
		return nil, errors.New("agent is nil")
	}
	a.operationMu.RLock()
	defer a.operationMu.RUnlock()
	if a.isClosing() {
		return nil, ErrClosed
	}
	run, err := a.runtime.Start(ctx, request)
	if err == nil && a.subagents != nil {
		a.subagents.watchParentRun(run)
	}
	return run, err
}

// StartSubscribed begins one run with a live subscription installed before
// RunStarted is committed. The returned subscription is live-only; use
// Run.EventsBetween to recover a retained range when reconnecting.
func (a *Agent) StartSubscribed(ctx context.Context, request agentruntime.Request) (*agentruntime.Run, agentruntime.EventSubscription, error) {
	if a == nil || a.runtime == nil {
		return nil, agentruntime.EventSubscription{}, errors.New("agent is nil")
	}
	a.operationMu.RLock()
	defer a.operationMu.RUnlock()
	if a.isClosing() {
		return nil, agentruntime.EventSubscription{}, ErrClosed
	}
	run, subscription, err := a.runtime.StartSubscribed(ctx, request)
	if err == nil && a.subagents != nil {
		a.subagents.watchParentRun(run)
	}
	return run, subscription, err
}

// SubscribeSubagentCallbacks returns a live-only stream of compact child-turn
// completions. Durable unread state remains available through ReadSubagent and
// context reminders when no subscriber is attached.
func (a *Agent) SubscribeSubagentCallbacks(ctx context.Context) <-chan SubagentCallback {
	if a == nil || a.subagents == nil {
		closed := make(chan SubagentCallback)
		close(closed)
		return closed
	}
	return a.subagents.subscribeCallbacks(ctx)
}

// ContinueSubagentCallbackSubscribed starts a parent turn from a trusted
// child completion callback and advances the child's observation cursor only
// after the turn was accepted. This keeps callback input distinct from human
// user messages while giving UIs the same pre-subscribed event stream.
func (a *Agent) ContinueSubagentCallbackSubscribed(ctx context.Context, callback SubagentCallback) (*agentruntime.Run, agentruntime.EventSubscription, error) {
	if a == nil || a.runtime == nil {
		return nil, agentruntime.EventSubscription{}, errors.New("agent is nil")
	}
	if callback.ParentSessionID == "" || callback.SubagentID == "" || callback.TurnID == "" {
		return nil, agentruntime.EventSubscription{}, errors.New("subagent callback identifiers are required")
	}
	manager, err := a.subagentManager()
	if err != nil {
		return nil, agentruntime.EventSubscription{}, err
	}
	run, subscription, err := a.StartSubscribed(ctx, agentruntime.Request{
		SessionID: callback.ParentSessionID,
		Message:   callback.RuntimeMessage(),
	})
	if err != nil {
		return nil, agentruntime.EventSubscription{}, err
	}
	// The callback itself carries the final answer, so observation failure does
	// not invalidate an already-running continuation. It only leaves the
	// durable fallback unread for a future turn.
	_ = manager.observeCallback(context.WithoutCancel(nonNilContext(ctx)), callback)
	return run, subscription, nil
}

// ResolvePermission passes a UI decision to the active run's executor.
func (a *Agent) ResolvePermission(ctx context.Context, decision permission.Decision) error {
	if a == nil || a.runtime == nil {
		return errors.New("agent is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.operationMu.RLock()
	defer a.operationMu.RUnlock()
	if a.isClosing() {
		return ErrClosed
	}
	resolveCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.closing, cancel)
	defer stop()
	defer cancel()
	err := a.runtime.ResolvePermission(resolveCtx, decision)
	if a.isClosing() {
		return ErrClosed
	}
	return err
}

// ResolveConfirmation passes a UI Yes/No answer to the active run's executor.
// Confirmation is independent from permission modes and grants.
func (a *Agent) ResolveConfirmation(ctx context.Context, decision confirmation.Decision) error {
	if a == nil || a.runtime == nil {
		return errors.New("agent is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.operationMu.RLock()
	defer a.operationMu.RUnlock()
	if a.isClosing() {
		return ErrClosed
	}
	resolveCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.closing, cancel)
	defer stop()
	defer cancel()
	err := a.runtime.ResolveConfirmation(resolveCtx, decision)
	if a.isClosing() {
		return ErrClosed
	}
	return err
}

// Runtime exposes the underlying runtime for advanced controls such as
// interruption. Normal callers should use StartSubscribed and ResolvePermission.
func (a *Agent) Runtime() *agentruntime.Runtime {
	if a == nil {
		return nil
	}
	return a.runtime
}

// ListMessages returns an independent, ordered snapshot of sessionID's
// transcript. Reads remain available after Close because the configured
// transcript store is retained and this method never exposes it for mutation.
func (a *Agent) ListMessages(ctx context.Context, sessionID string) ([]agentruntime.Message, error) {
	if a == nil || a.messages == nil {
		return nil, errors.New("agent is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	messages, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return storage.CloneMessages(messages), nil
}

// SubagentDefinitions returns the immutable project-defined catalog available
// to this root agent. Child agents expose no management catalog.
func (a *Agent) SubagentDefinitions() []SubagentDefinition {
	if a == nil || a.subagents == nil || a.subagents.project == nil {
		return nil
	}
	return a.subagents.project.Subagents()
}

// Definitions is a compact alias for SubagentDefinitions for transports that
// expose the subagent catalog directly.
func (a *Agent) Definitions() []SubagentDefinition { return a.SubagentDefinitions() }

// ListSubagents lists instances owned by parentSessionID.
func (a *Agent) ListSubagents(ctx context.Context, parentSessionID string, includeClosed bool) ([]storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return nil, err
	}
	return manager.List(ctx, parentSessionID, includeClosed)
}

// StartSubagent starts a project-defined child asynchronously.
func (a *Agent) StartSubagent(ctx context.Context, parentSessionID, parentTurnID, name, message, label string) (storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return storage.Subagent{}, err
	}
	return manager.Start(ctx, parentSessionID, parentTurnID, name, message, label)
}

// SubagentReadResult is the compact final-answer result returned by the
// application recovery API. It is deliberately not exposed as a model tool.
type SubagentReadResult struct {
	Subagent      storage.Subagent
	FinalAnswer   *agentruntime.Message
	NextMessageID string
}

// ReadSubagent returns the latest final assistant answer after a cursor and
// advances the owned child's durable observation cursor.
func (a *Agent) ReadSubagent(ctx context.Context, parentSessionID, subagentID, afterMessageID string) (SubagentReadResult, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return SubagentReadResult{}, err
	}
	return manager.Read(ctx, parentSessionID, subagentID, afterMessageID)
}

// WaitSubagent waits for owned child activity or lifecycle changes.
func (a *Agent) WaitSubagent(ctx context.Context, parentSessionID string, subagentIDs []string, after map[string]uint64) ([]storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return nil, err
	}
	return manager.Wait(ctx, parentSessionID, subagentIDs, after)
}

// SendSubagentMessage queues running child work or resumes an idle child after
// its latest callback has been consumed.
func (a *Agent) SendSubagentMessage(ctx context.Context, parentSessionID, subagentID, message string) (storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return storage.Subagent{}, err
	}
	return manager.Send(ctx, parentSessionID, subagentID, message)
}

// CloseSubagent closes one owned, completed or failed child after its latest
// callback has been consumed, and retains its transcript history.
func (a *Agent) CloseSubagent(ctx context.Context, parentSessionID, subagentID string) (storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return storage.Subagent{}, err
	}
	return manager.CloseSubagent(ctx, parentSessionID, subagentID)
}

// ForceCloseSubagent interrupts and closes one owned child regardless of its
// current outcome or callback state. Callers should expose this only for an
// explicit user-directed destructive action.
func (a *Agent) ForceCloseSubagent(ctx context.Context, parentSessionID, subagentID string) (storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return storage.Subagent{}, err
	}
	result, err := manager.ForceCloseSubagent(ctx, parentSessionID, subagentID)
	return result.Subagent, err
}

// SubagentRun returns an ownership-checked retained child run.
func (a *Agent) SubagentRun(ctx context.Context, parentSessionID, subagentID, turnID string) (*agentruntime.Run, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return nil, err
	}
	return manager.Run(ctx, parentSessionID, subagentID, turnID)
}

// InterruptSubagent interrupts the active turn of one owned child.
func (a *Agent) InterruptSubagent(ctx context.Context, parentSessionID, subagentID, reason string) error {
	manager, err := a.subagentManager()
	if err != nil {
		return err
	}
	return manager.Interrupt(ctx, parentSessionID, subagentID, reason)
}

// ResolveSubagentPermission routes a permission decision to the Agent that
// owns the child session, after enforcing parent ownership.
func (a *Agent) ResolveSubagentPermission(ctx context.Context, parentSessionID, subagentID string, decision permission.Decision) error {
	manager, err := a.subagentManager()
	if err != nil {
		return err
	}
	if _, err := manager.getOwned(nonNilContext(ctx), parentSessionID, subagentID); err != nil {
		return err
	}
	instance, err := manager.instance(subagentID)
	if err != nil {
		return err
	}
	return instance.agent.ResolvePermission(ctx, decision)
}

// ResolveSubagentConfirmation routes a Yes/No answer to an owned child.
func (a *Agent) ResolveSubagentConfirmation(ctx context.Context, parentSessionID, subagentID string, decision confirmation.Decision) error {
	manager, err := a.subagentManager()
	if err != nil {
		return err
	}
	if _, err := manager.getOwned(nonNilContext(ctx), parentSessionID, subagentID); err != nil {
		return err
	}
	instance, err := manager.instance(subagentID)
	if err != nil {
		return err
	}
	return instance.agent.ResolveConfirmation(ctx, decision)
}

func (a *Agent) subagentManager() (*subagentManager, error) {
	if a == nil || a.subagents == nil {
		return nil, errors.New("subagent management is not configured")
	}
	return a.subagents, nil
}

func (a *Agent) isClosing() bool {
	select {
	case <-a.closing.Done():
		return true
	default:
		return false
	}
}
