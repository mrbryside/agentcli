// Package agentcli assembles the runtime and tool executor needed by an agent
// user interface. It intentionally keeps transport channels private while
// leaving Runtime available for advanced per-run controls and subscriptions.
package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	langfuseobs "github.com/mrbryside/agentcli/observability/langfuse"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
	"github.com/mrbryside/agentcli/toolexecution"
)

// Agent owns one runtime and its private tool executor.
type Agent struct {
	runtime       *agentruntime.Runtime
	model         agentruntime.Model
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

	langfuse     *langfuseobs.Client
	ownsLangfuse bool

	subagents      *subagentManager
	responseScopes *toolexecution.ResponseScopeCoordinator
}

func taskAgentsForProject(project *Project) []toolexecution.TaskAgent {
	if project == nil {
		return nil
	}
	definitions := project.Subagents()
	agents := make([]toolexecution.TaskAgent, 0, len(definitions))
	for _, definition := range definitions {
		// Project validation has already ensured the definition and each of its
		// declared capabilities exist. Keeping this projection here makes the
		// task tool's advertised catalog match the manager's start targets.
		agents = append(agents, toolexecution.TaskAgent{
			Name:        definition.Name,
			Description: definition.Description,
		})
	}
	return agents
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
	if configuration.project != nil && !configuration.subagentAgent {
		if err := validateProjectToolAllowlists(configuration.project, configuration.tools); err != nil {
			return nil, err
		}
	}
	if configuration.subagentAgent {
		if err := validateSubagentTools(configuration.tools); err != nil {
			return nil, err
		}
	}
	// Projects without definitions deliberately allocate no subagent-session
	// resources. A subagent itself is also not a main agent manager, keeping the
	// initial nesting depth at one.
	if configuration.project != nil && len(configuration.project.subagents) != 0 && !configuration.subagentAgent {
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
	inputGuardModel, err := configuration.resolveGuardModel(configuration.inputGuardProvider, configuration.inputGuardModel)
	if err != nil {
		return nil, fmt.Errorf("resolve input guard model: %w", err)
	}
	outputGuardModel, err := configuration.resolveGuardModel(configuration.outputGuardProvider, configuration.outputGuardModel)
	if err != nil {
		return nil, fmt.Errorf("resolve output guard model: %w", err)
	}
	policyController, err := toolexecution.NewPermissionController(configuration.permissionPolicy)
	if err != nil {
		return nil, fmt.Errorf("create permission controller: %w", err)
	}

	registry := toolexecution.NewRegistry()
	var taskTools *toolexecution.TaskToolBridge
	mainAgentHasSubagents := configuration.project != nil && len(configuration.project.subagents) != 0 && !configuration.subagentAgent
	if mainAgentHasSubagents {
		for _, tool := range configuration.tools {
			if toolexecution.IsSubagentToolName(tool.Definition.Name) {
				return nil, fmt.Errorf("custom tool %q conflicts with reserved subagent tool", tool.Definition.Name)
			}
		}
		// Definitions are loaded only from this project's configured catalog.
		// The task bridge receives no caller-supplied names, so its advertised
		// names match the manager's start targets exactly.
		taskTools = toolexecution.NewTaskToolBridge(taskAgentsForProject(configuration.project))
	}
	// Keep configuration.tools as caller-provided tools only. The skill loader
	// is a per-Agent framework tool; retaining it in config would make subagent
	// construction inherit it and register a duplicate loader of its own.
	runtimeTools := configuration.tools
	if configuration.project != nil && !configuration.subagentAgent {
		runtimeTools = configuration.project.filterMainAgentTools(configuration.tools)
	}
	if err := validateToolRequiredSkills(configuration.project, runtimeTools); err != nil {
		return nil, err
	}
	registeredTools := append([]toolexecution.Tool(nil), runtimeTools...)
	if configuration.project != nil && len(configuration.project.skills) != 0 {
		registeredTools = append(registeredTools, toolexecution.NewSkillLoader(
			configuration.project.executionSkills(),
			configuration.messages,
			configuration.skillReload,
		).Tool())
	}
	for _, tool := range registeredTools {
		if err := registry.Register(tool); err != nil {
			return nil, fmt.Errorf("register tool: %w", err)
		}
	}
	if taskTools != nil {
		for _, tool := range taskTools.Tools() {
			if err := registry.Register(tool); err != nil {
				return nil, fmt.Errorf("register task tool: %w", err)
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

	langfuseClient := configuration.langfuse
	ownsLangfuse := false
	if langfuseClient == nil {
		var enabled bool
		langfuseClient, enabled, err = newProjectLangfuse(runContext, configuration.project)
		if err != nil {
			cancel()
			return nil, err
		}
		ownsLangfuse = enabled
	}
	if langfuseClient != nil {
		configuration.model = langfuseClient.ObserveModel(configuration.model)
		configuration.compactionModel = langfuseClient.ObserveModel(configuration.compactionModel)
		inputGuardModel = langfuseClient.ObserveModel(inputGuardModel)
		outputGuardModel = langfuseClient.ObserveModel(outputGuardModel)
		configuration.langfuse = langfuseClient
	}

	closing, closeSignal := context.WithCancel(context.Background())
	agent := &Agent{
		model: configuration.model, messages: configuration.messages, project: configuration.project,
		context: runContext, cancel: cancel, closing: closing, closingCancel: closeSignal,
		executorDone: make(chan struct{}), responseScopes: toolexecution.NewResponseScopeCoordinator(runContext),
		langfuse: langfuseClient, ownsLangfuse: ownsLangfuse,
	}
	agent.responseScopes.SetLogger(configuration.logger)
	var manager *subagentManager
	if mainAgentHasSubagents {
		manager, err = newSubagentManager(agent, configuration)
		if err != nil {
			closeSignal()
			cancel()
			shutdownOwnedLangfuse(langfuseClient, ownsLangfuse)
			return nil, fmt.Errorf("create subagent manager: %w", err)
		}
		taskTools.Bind(func(ctx context.Context, invocation toolexecution.Invocation, input toolexecution.TaskToolInput) (json.RawMessage, error) {
			request := TaskRequest{
				MainAgentSessionID: invocation.SessionID,
				MainAgentTurnID:    invocation.TurnID,
				Prompt:             input.Prompt,
				Background:         input.Background,
			}
			if input.Agent != nil {
				request.AgentName = *input.Agent
			}
			if input.Description != nil {
				request.Description = *input.Description
			}
			if input.TaskID != nil {
				request.TaskID = *input.TaskID
			}
			result, executeErr := manager.ExecuteTask(ctx, request)
			if executeErr != nil {
				return nil, executeErr
			}
			return json.Marshal(result)
		})
		agent.responseScopes.SetCleanup(manager.autoCloseScopeSubagents)
	}
	reminderProvider := composeContextReminderProviders(
		newTurnContextReminderProvider(),
		configuration.contextReminderProvider,
	)
	requiredAtTurnEnd := make([]string, 0)
	requiredAtResponseScopeEnd := make([]string, 0)
	for _, tool := range registeredTools {
		switch tool.Trigger {
		case toolexecution.EndTurn:
			requiredAtTurnEnd = append(requiredAtTurnEnd, tool.Definition.Name)
		case toolexecution.EndResponseScope:
			requiredAtResponseScopeEnd = append(requiredAtResponseScopeEnd, tool.Definition.Name)
		}
	}
	stepLimitFinalizationTools := append([]string(nil), requiredAtTurnEnd...)
	stepLimitFinalizationTools = append(stepLimitFinalizationTools, requiredAtResponseScopeEnd...)
	if configuration.subagentAgent {
		// A subagent reaches its step limit with a text-only final response.
		stepLimitFinalizationTools = nil
	}
	completionGuard := completionGuardWithRequiredTools(
		nil,
		requiredAtTurnEnd,
		requiredAtResponseScopeEnd,
		agent.responseScopes.ReadyToEnd,
	)
	if manager != nil {
		reminderProvider = composeContextReminderProviders(reminderProvider, subagentReminderProvider(manager))
	}

	var compactor *agentruntime.Compactor
	if configuration.compactionModel != nil {
		compactor = &agentruntime.Compactor{Model: configuration.compactionModel, Estimator: configuration.contextEstimator}
	}
	runtime, err := agentruntime.New(runContext, agentruntime.Config{
		Model:                      configuration.model,
		Messages:                   configuration.messages,
		SystemPrompts:              append([]string(nil), configuration.systemPrompts...),
		ContextReminderProvider:    reminderProvider,
		CompletionGuard:            completionGuard,
		InputGuard:                 configuration.inputGuard,
		OutputGuard:                configuration.outputGuard,
		InputGuardPrompt:           configuration.inputGuardPrompt,
		OutputGuardPrompt:          configuration.outputGuardPrompt,
		InputGuardModel:            inputGuardModel,
		OutputGuardModel:           outputGuardModel,
		Tools:                      registry.Definitions(),
		StepLimitFinalizationTools: stepLimitFinalizationTools,
		ToolRequests:               toolRequests,
		ToolResults:                toolResults,
		ToolInterrupts:             toolInterrupts,
		PermissionRequests:         permissionRequests,
		PermissionDecisions:        permissionDecisions,
		ConfirmationRequests:       confirmationRequests,
		ConfirmationDecisions:      confirmationDecisions,
		PermissionMode:             configuration.permissionMode,
		MaxSteps:                   configuration.maxProviderSteps,
		Compactor:                  compactor,
		Logger:                     configuration.logger,
		PermissionModeChanged: func(_, current permission.Mode) error {
			return policyController.SetMode(current)
		},
	})
	if err != nil {
		closeSignal()
		cancel()
		shutdownOwnedLangfuse(langfuseClient, ownsLangfuse)
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
		ToolCallGuardModel:    configuration.model,
		ToolCallGuardTimeout:  configuration.toolCallGuardTimeout,
		ResponseScopes:        agent.responseScopes,
		Messages:              configuration.messages,
		ToolCallGuardModelResolver: func(providerName, modelName string) (agentruntime.Model, error) {
			if configuration.project == nil {
				return nil, errors.New("tool-call guard provider requires a project with provider profiles")
			}
			model, modelErr := configuration.project.ModelFor(providerName, modelName)
			if modelErr != nil {
				return nil, modelErr
			}
			return langfuseClient.ObserveModel(model), nil
		},
	})
	if err != nil {
		closeSignal()
		cancel()
		shutdownOwnedLangfuse(langfuseClient, ownsLangfuse)
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
	registered := make(map[string]toolexecution.Tool, len(tools))
	for _, tool := range tools {
		registered[tool.Definition.Name] = tool
	}
	if project.restrictTools {
		for _, name := range project.toolNames {
			if _, found := registered[name]; !found {
				return fmt.Errorf("main agent requires custom tool %q, but it is not registered", name)
			}
		}
	}
	for _, definition := range project.Subagents() {
		availableSkills := make(map[string]struct{}, len(definition.Skills))
		for _, name := range definition.Skills {
			availableSkills[name] = struct{}{}
		}
		for _, name := range definition.Tools {
			tool, found := registered[name]
			if !found {
				return fmt.Errorf("subagent %q requires custom tool %q, but it is not registered", definition.Name, name)
			}
			if tool.Trigger == toolexecution.EndResponseScope {
				return fmt.Errorf(
					"subagent %q cannot use custom tool %q: EndResponseScope tools are supported only by main agents",
					definition.Name,
					name,
				)
			}
			for _, requiredSkill := range tool.RequiredSkills {
				if _, available := availableSkills[requiredSkill]; !available {
					return fmt.Errorf(
						"subagent %q uses custom tool %q which requires skill %q, but that skill is not available to the subagent",
						definition.Name,
						name,
						requiredSkill,
					)
				}
			}
		}
	}
	return nil
}

func validateSubagentTools(tools []toolexecution.Tool) error {
	for _, tool := range tools {
		if tool.Definition.Name == toolexecution.TaskToolName {
			return errors.New("subagent cannot use the main-agent task tool")
		}
		if tool.Trigger == toolexecution.EndResponseScope {
			return fmt.Errorf(
				"subagent cannot use custom tool %q: EndResponseScope tools are supported only by main agents",
				tool.Definition.Name,
			)
		}
	}
	return nil
}

func validateToolRequiredSkills(project *Project, tools []toolexecution.Tool) error {
	for _, tool := range tools {
		for _, name := range tool.RequiredSkills {
			if project == nil {
				return fmt.Errorf(
					"custom tool %q requires skill %q, but no project skills are configured",
					tool.Definition.Name,
					name,
				)
			}
			if _, found := project.skills[name]; !found {
				return fmt.Errorf(
					"custom tool %q requires skill %q, but it is not available to this agent",
					tool.Definition.Name,
					name,
				)
			}
		}
	}
	return nil
}

func (project *Project) filterMainAgentTools(tools []toolexecution.Tool) []toolexecution.Tool {
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
	if err := ensureResponseTurnID(&request); err != nil {
		return nil, err
	}
	finishReminderReservation := a.reserveAutoClosedSubagentReminder(request)
	if err := a.responseScopes.BeginMainAgentTurn(request.SessionID, request.TurnID); err != nil {
		finishReminderReservation(false)
		return nil, err
	}
	run, err := a.runtime.Start(ctx, request)
	if err != nil {
		a.responseScopes.RollbackMainAgentTurn(request.SessionID, request.TurnID)
		finishReminderReservation(false)
		return nil, err
	}
	finishReminderReservation(true)
	a.watchAcceptedRun(run)
	return run, nil
}

// SendMessage starts one user turn in sessionID and returns a live event
// subscription that includes RunStarted. The runtime generates the turn ID,
// message ID, and timestamp. Reusing sessionID continues its stored
// conversation; starting another turn while that session is active returns
// ErrTurnInProgress.
func (a *Agent) SendMessage(ctx context.Context, sessionID, message string) (*Run, EventSubscription, error) {
	return a.StartSubscribed(ctx, agentruntime.Request{
		SessionID: sessionID,
		Message: agentruntime.Message{
			Type:    agentruntime.MessageTypeUser,
			Content: message,
		},
	})
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
	if err := ensureResponseTurnID(&request); err != nil {
		return nil, agentruntime.EventSubscription{}, err
	}
	finishReminderReservation := a.reserveAutoClosedSubagentReminder(request)
	if err := a.responseScopes.BeginMainAgentTurn(request.SessionID, request.TurnID); err != nil {
		finishReminderReservation(false)
		return nil, agentruntime.EventSubscription{}, err
	}
	run, subscription, err := a.runtime.StartSubscribed(ctx, request)
	if err != nil {
		a.responseScopes.RollbackMainAgentTurn(request.SessionID, request.TurnID)
		finishReminderReservation(false)
		return nil, agentruntime.EventSubscription{}, err
	}
	finishReminderReservation(true)
	a.watchAcceptedRun(run)
	return run, subscription, nil
}

// SubscribeSubagentResults returns a live-only stream of compact subagent-turn
// completions. Durable unread state remains available through ReadSubagent and
// context reminders when no subscriber is attached.
func (a *Agent) SubscribeSubagentResults(ctx context.Context) <-chan SubagentResult {
	if a == nil || a.subagents == nil {
		closed := make(chan SubagentResult)
		close(closed)
		return closed
	}
	return a.subagents.subscribeResults(ctx)
}

// SubscribeSystemEvents returns a live-only stream of agent-level facts that
// are not owned by one runtime turn.
func (a *Agent) SubscribeSystemEvents(ctx context.Context) <-chan SystemEvent {
	if a == nil || a.subagents == nil {
		closed := make(chan SystemEvent)
		close(closed)
		return closed
	}
	return a.subagents.subscribeSystemEvents(ctx)
}

// SubscribeScopeEvents returns a live-only stream containing
// PreEndScope immediately before scope cleanup and EndScope after all final
// EndResponseScope tool handlers have run and the scope has been removed.
func (a *Agent) SubscribeScopeEvents(ctx context.Context) <-chan ScopeEvent {
	if a == nil || a.responseScopes == nil {
		closed := make(chan ScopeEvent)
		close(closed)
		return closed
	}
	return a.responseScopes.SubscribeEvents(ctx)
}

// SubscribeSubagentConfirmations returns live subagent confirmation lifecycle
// events addressed to main agent sessions. Call PendingSubagentConfirmations when
// attaching or reconnecting so a request cannot be missed.
func (a *Agent) SubscribeSubagentConfirmations(ctx context.Context) <-chan SubagentConfirmationEvent {
	if a == nil || a.subagents == nil {
		closed := make(chan SubagentConfirmationEvent)
		close(closed)
		return closed
	}
	return a.subagents.subscribeConfirmations(ctx)
}

// PendingSubagentConfirmations returns durable pending confirmation requests
// for subagents owned by mainAgentSessionID.
func (a *Agent) PendingSubagentConfirmations(ctx context.Context, mainAgentSessionID string) ([]SubagentConfirmationEvent, error) {
	if a == nil || a.subagents == nil {
		return []SubagentConfirmationEvent{}, nil
	}
	manager, err := a.subagentManager()
	if err != nil {
		return nil, err
	}
	return manager.pendingConfirmations(nonNilContext(ctx), mainAgentSessionID)
}

// SubscribeSubagentPermissions returns live subagent permission lifecycle events
// addressed to main agent sessions. Call PendingSubagentPermissions when attaching
// or reconnecting so a request cannot be missed.
func (a *Agent) SubscribeSubagentPermissions(ctx context.Context) <-chan SubagentPermissionEvent {
	if a == nil || a.subagents == nil {
		closed := make(chan SubagentPermissionEvent)
		close(closed)
		return closed
	}
	return a.subagents.subscribePermissions(ctx)
}

// PendingSubagentPermissions returns durable pending permission requests for
// subagents owned by mainAgentSessionID.
func (a *Agent) PendingSubagentPermissions(ctx context.Context, mainAgentSessionID string) ([]SubagentPermissionEvent, error) {
	if a == nil || a.subagents == nil {
		return []SubagentPermissionEvent{}, nil
	}
	manager, err := a.subagentManager()
	if err != nil {
		return nil, err
	}
	return manager.pendingPermissions(nonNilContext(ctx), mainAgentSessionID)
}

// ContinueSubagentResultSubscribed starts a main agent turn from a trusted
// subagent completion result and advances the subagent's observation cursor only
// after the turn was accepted. This keeps result input distinct from human
// user messages while giving UIs the same pre-subscribed event stream.
//
// Call TryInjectSubagentResult first when the host wants results to join
// an already-active main agent between provider rounds.
func (a *Agent) ContinueSubagentResultSubscribed(ctx context.Context, result SubagentResult) (*agentruntime.Run, agentruntime.EventSubscription, error) {
	if a == nil || a.runtime == nil {
		return nil, agentruntime.EventSubscription{}, errors.New("agent is nil")
	}
	if result.MainAgentSessionID == "" || result.SubagentID == "" || result.SubagentTurnID == "" {
		return nil, agentruntime.EventSubscription{}, errors.New("subagent result identifiers are required")
	}
	continuationTurnID, err := newSubagentID("turn_")
	if err != nil {
		return nil, agentruntime.EventSubscription{}, fmt.Errorf("create result continuation turn: %w", err)
	}
	return a.continueSubagentResultSubscribed(ctx, result, continuationTurnID)
}

// TryInjectSubagentResult delivers a subagent result into the next provider
// boundary of the currently active main agent response scope. It returns false
// when no compatible main agent run is active, allowing the host to start a normal
// result continuation turn instead.
func (a *Agent) TryInjectSubagentResult(ctx context.Context, result SubagentResult) (bool, error) {
	if a == nil || a.runtime == nil {
		return false, errors.New("agent is nil")
	}
	if result.MainAgentSessionID == "" || result.SubagentID == "" || result.SubagentTurnID == "" {
		return false, errors.New("subagent result identifiers are required")
	}
	manager, err := a.subagentManager()
	if err != nil {
		return false, err
	}
	activeTurnID, active := a.runtime.ActiveTurnID(result.MainAgentSessionID)
	if !active {
		return false, nil
	}
	reservation, err := a.responseScopes.ReserveInlineResultWithMetadata(
		result.MainAgentSessionID,
		activeTurnID,
		responseScopeResult(result),
	)
	if err != nil {
		if errors.Is(err, toolexecution.ErrResponseScopeAssignmentNotFound) ||
			strings.Contains(err.Error(), "different response scope") ||
			strings.Contains(err.Error(), "does not belong to a response scope") {
			return false, nil
		}
		return false, err
	}
	injectCtx, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(a.closing, cancel)
	defer func() {
		stop()
		cancel()
	}()
	err = a.runtime.InjectRuntimeMessage(
		injectCtx,
		result.MainAgentSessionID,
		activeTurnID,
		result.RuntimeMessage(reservation.ResultProgress()),
		reservation.Commit,
	)
	if err != nil {
		reservation.Rollback(result.SubagentID, result.SubagentTurnID)
		if errors.Is(err, agentruntime.ErrRunNotFound) {
			return false, nil
		}
		if a.isClosing() {
			return false, ErrClosed
		}
		return false, err
	}
	_ = manager.observeSubagentResult(context.WithoutCancel(nonNilContext(ctx)), result)
	return true, nil
}

func (a *Agent) continueSubagentResultSubscribed(ctx context.Context, result SubagentResult, continuationTurnID string) (*agentruntime.Run, agentruntime.EventSubscription, error) {
	if a == nil || a.runtime == nil {
		return nil, agentruntime.EventSubscription{}, errors.New("agent is nil")
	}
	if result.MainAgentSessionID == "" || result.SubagentID == "" || result.SubagentTurnID == "" {
		return nil, agentruntime.EventSubscription{}, errors.New("subagent result identifiers are required")
	}
	if continuationTurnID == "" {
		return nil, agentruntime.EventSubscription{}, errors.New("result continuation turn ID is required")
	}
	manager, err := a.subagentManager()
	if err != nil {
		return nil, agentruntime.EventSubscription{}, err
	}
	reservation, err := a.responseScopes.ReserveResultTurnWithMetadata(
		result.MainAgentSessionID,
		continuationTurnID,
		responseScopeResult(result),
	)
	if err != nil {
		return nil, agentruntime.EventSubscription{}, err
	}
	run, subscription, err := a.runtime.StartSubscribed(ctx, agentruntime.Request{
		SessionID: result.MainAgentSessionID,
		TurnID:    continuationTurnID,
		Message:   result.RuntimeMessage(reservation.ResultProgress()),
	})
	if err != nil {
		reservation.Rollback(result.SubagentID, result.SubagentTurnID)
		return nil, agentruntime.EventSubscription{}, err
	}
	reservation.Commit()
	// The result itself carries the final answer, so observation failure does
	// not invalidate an already-running continuation. It only leaves the
	// durable fallback unread for a future turn.
	_ = manager.observeSubagentResult(context.WithoutCancel(nonNilContext(ctx)), result)
	// Install the response-scope completion watcher only after observation so
	// a very fast result continuation cannot end its scope first.
	a.watchAcceptedRun(run)
	return run, subscription, nil
}

func responseScopeResult(result SubagentResult) toolexecution.ResponseScopeDeliveredResult {
	return toolexecution.ResponseScopeDeliveredResult{
		SubagentID:     result.SubagentID,
		DefinitionName: result.DefinitionName,
		DisplayName:    result.DisplayName,
		SubagentTurnID: result.SubagentTurnID,
		ResultStatus:   string(result.Status),
	}
}

func ensureResponseTurnID(request *agentruntime.Request) error {
	if request == nil {
		return errors.New("request is required")
	}
	if request.TurnID != "" {
		return nil
	}
	if request.Message.TurnID != "" {
		request.TurnID = request.Message.TurnID
		return nil
	}
	turnID, err := newSubagentID("turn_")
	if err != nil {
		return fmt.Errorf("create response turn: %w", err)
	}
	request.TurnID = turnID
	return nil
}

func (a *Agent) reserveAutoClosedSubagentReminder(request agentruntime.Request) func(bool) {
	if a == nil || a.subagents == nil || request.Message.Type != agentruntime.MessageTypeUser {
		return func(bool) {}
	}
	return a.subagents.reserveAutoClosedSubagentReminder(request.SessionID, request.TurnID)
}

func (a *Agent) watchAcceptedRun(run *agentruntime.Run) {
	if run == nil {
		return
	}
	if a.subagents != nil {
		a.subagents.watchMainAgentRun(run)
	}
	subscription := run.Subscribe(context.Background())
	go func() {
		for range subscription.Events {
		}
		a.responseScopes.FinishTurn(run.SessionID(), run.TurnID())
		if a.subagents != nil {
			a.subagents.finishAutoClosedSubagentReminder(run.SessionID(), run.TurnID())
		}
	}()
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
// to this main agent. Subagents expose no management catalog.
func (a *Agent) SubagentDefinitions() []SubagentDefinition {
	if a == nil || a.subagents == nil || a.subagents.project == nil {
		return nil
	}
	return a.subagents.project.Subagents()
}

// Definitions is a compact alias for SubagentDefinitions for transports that
// expose the subagent catalog directly.
func (a *Agent) Definitions() []SubagentDefinition { return a.SubagentDefinitions() }

// ListSubagents lists instances owned by mainAgentSessionID.
func (a *Agent) ListSubagents(ctx context.Context, mainAgentSessionID string, includeClosed bool) ([]storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return nil, err
	}
	return manager.List(ctx, mainAgentSessionID, includeClosed)
}

// StartSubagent starts a project-defined subagent asynchronously.
func (a *Agent) StartSubagent(ctx context.Context, mainAgentSessionID, mainAgentTurnID, name, message, label string) (storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return storage.Subagent{}, err
	}
	return manager.Start(ctx, mainAgentSessionID, mainAgentTurnID, name, message, label)
}

// SubagentReadResult is the compact final-answer result returned by the
// application recovery API. It is deliberately not exposed as a model tool.
type SubagentReadResult struct {
	Subagent      storage.Subagent
	FinalAnswer   *agentruntime.Message
	LastMessageID string
}

// ReadSubagent returns the latest final assistant answer after a cursor and
// advances the owned subagent's durable observation cursor.
func (a *Agent) ReadSubagent(ctx context.Context, mainAgentSessionID, subagentID, afterMessageID string) (SubagentReadResult, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return SubagentReadResult{}, err
	}
	return manager.Read(ctx, mainAgentSessionID, subagentID, afterMessageID)
}

// WaitSubagent waits for owned subagent activity or lifecycle changes.
func (a *Agent) WaitSubagent(ctx context.Context, mainAgentSessionID string, subagentIDs []string, after map[string]uint64) ([]storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return nil, err
	}
	return manager.Wait(ctx, mainAgentSessionID, subagentIDs, after)
}

// SendSubagentMessage queues running subagent work or resumes an idle subagent after
// its latest result has been consumed.
func (a *Agent) SendSubagentMessage(ctx context.Context, mainAgentSessionID, subagentID, message string) (storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return storage.Subagent{}, err
	}
	return manager.Send(ctx, mainAgentSessionID, subagentID, message)
}

// CloseSubagent destructively closes one owned subagent, interrupts active work,
// drops queued input, cancels its outstanding response-scope result
// obligations, and retains transcript history. Applications should bind it to
// an explicit user or operator action.
func (a *Agent) CloseSubagent(ctx context.Context, mainAgentSessionID, subagentID string) (storage.Subagent, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return storage.Subagent{}, err
	}
	result, err := manager.CloseSubagent(ctx, mainAgentSessionID, subagentID)
	return result.Subagent, err
}

// SubagentRun returns an ownership-checked retained subagent run.
func (a *Agent) SubagentRun(ctx context.Context, mainAgentSessionID, subagentID, turnID string) (*agentruntime.Run, error) {
	manager, err := a.subagentManager()
	if err != nil {
		return nil, err
	}
	return manager.Run(ctx, mainAgentSessionID, subagentID, turnID)
}

// InterruptSubagent interrupts the active turn of one owned subagent.
func (a *Agent) InterruptSubagent(ctx context.Context, mainAgentSessionID, subagentID, reason string) error {
	manager, err := a.subagentManager()
	if err != nil {
		return err
	}
	return manager.Interrupt(ctx, mainAgentSessionID, subagentID, reason)
}

// ResolveSubagentPermission routes a permission decision to the Agent that
// owns the subagent session, after enforcing main agent ownership.
func (a *Agent) ResolveSubagentPermission(ctx context.Context, mainAgentSessionID, subagentID string, decision permission.Decision) error {
	manager, err := a.subagentManager()
	if err != nil {
		return err
	}
	if _, err := manager.getOwned(nonNilContext(ctx), mainAgentSessionID, subagentID); err != nil {
		return err
	}
	instance, err := manager.instance(subagentID)
	if err != nil {
		return err
	}
	return instance.agent.ResolvePermission(ctx, decision)
}

// ResolveSubagentConfirmation routes a Yes/No answer to an owned subagent.
func (a *Agent) ResolveSubagentConfirmation(ctx context.Context, mainAgentSessionID, subagentID string, decision confirmation.Decision) error {
	manager, err := a.subagentManager()
	if err != nil {
		return err
	}
	if _, err := manager.getOwned(nonNilContext(ctx), mainAgentSessionID, subagentID); err != nil {
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
