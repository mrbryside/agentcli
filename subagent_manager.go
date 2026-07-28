package agentcli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/toolexecution"
)

// subagentManager is intentionally package-private. The later tool and HTTP
// layers are thin adapters over this owner of subagent runtimes and the durable
// main agent/subagent relationship records.
type subagentManager struct {
	mainAgent *Agent
	store     storage.SubagentStorage
	project   *Project
	config    config
	ctx       context.Context

	mu              sync.RWMutex
	startMu         sync.Mutex // makes the per-mainAgent open-instance quota atomic.
	instances       map[string]*managedSubagent
	closed          bool
	changed         chan struct{}
	subagentFactory func(SubagentDefinition) (*Agent, error)

	resultMu             sync.Mutex
	nextResultSubscriber uint64
	resultSubscribers    map[uint64]*subagentResultSubscriber
	resultsClosed        bool

	reminderMu        sync.Mutex
	pendingAutoClosed map[string][]autoClosedSubagentNotice
	turnAutoClosed    map[subagentReminderKey][]autoClosedSubagentNotice

	confirmationMu             sync.Mutex
	nextConfirmationSubscriber uint64
	confirmationSubscribers    map[uint64]*subagentConfirmationSubscriber
	confirmationsClosed        bool

	permissionMu             sync.Mutex
	nextPermissionSubscriber uint64
	permissionSubscribers    map[uint64]*subagentPermissionSubscriber
	permissionsClosed        bool

	systemEventMu             sync.Mutex
	nextSystemEventSubscriber uint64
	systemEventSubscribers    map[uint64]*systemEventSubscriber
	systemEventsClosed        bool
}

type managedSubagent struct {
	// agent is cleared as soon as a subagent closes. runs intentionally remains:
	// completed event history is transport state, not a live subagent runtime.
	agent *Agent

	mu                            sync.Mutex // serializes the one active subagent turn and its mailbox.
	run                           *agentruntime.Run
	runs                          map[string]*agentruntime.Run
	lastAssignmentMainAgentTurnID string
	lastAssignmentKey             string
	lastStatusMainAgentTurnID     string
	lastStatusSnapshot            storage.Subagent
}

type subagentReadResult = SubagentReadResult

func newSubagentManager(mainAgent *Agent, configuration config) (*subagentManager, error) {
	if mainAgent == nil || configuration.project == nil || configuration.subagents == nil {
		return nil, errors.New("subagent manager requires mainAgent, project, and storage")
	}
	if configuration.maxSubagents <= 0 {
		return nil, errors.New("subagent maximum must be positive")
	}
	return &subagentManager{
		mainAgent: mainAgent, store: configuration.subagents, project: configuration.project,
		config: configuration, ctx: mainAgent.context, instances: make(map[string]*managedSubagent),
		changed: make(chan struct{}), resultSubscribers: make(map[uint64]*subagentResultSubscriber),
		pendingAutoClosed:       make(map[string][]autoClosedSubagentNotice),
		turnAutoClosed:          make(map[subagentReminderKey][]autoClosedSubagentNotice),
		confirmationSubscribers: make(map[uint64]*subagentConfirmationSubscriber),
		permissionSubscribers:   make(map[uint64]*subagentPermissionSubscriber),
		systemEventSubscribers:  make(map[uint64]*systemEventSubscriber),
	}, nil
}

// Start creates a separately-addressable subagent session and begins its first
// turn without waiting for provider completion.
func (m *subagentManager) Start(ctx context.Context, mainAgentSessionID, mainAgentTurnID, name, message, label string) (storage.Subagent, error) {
	ctx, definition, message, err := m.prepareStart(ctx, mainAgentSessionID, mainAgentTurnID, name, message)
	if err != nil {
		return storage.Subagent{}, err
	}
	m.startMu.Lock()
	defer m.startMu.Unlock()
	existing, err := m.store.ListByMainAgent(ctx, mainAgentSessionID)
	if err != nil {
		return storage.Subagent{}, err
	}
	return m.startLocked(ctx, mainAgentSessionID, mainAgentTurnID, message, label, definition, existing)
}

func (m *subagentManager) prepareStart(ctx context.Context, mainAgentSessionID, mainAgentTurnID, name, message string) (context.Context, SubagentDefinition, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ctx, SubagentDefinition{}, "", err
	}
	if strings.TrimSpace(mainAgentSessionID) == "" || strings.TrimSpace(mainAgentTurnID) == "" {
		return ctx, SubagentDefinition{}, "", errors.New("main agent session and turn IDs are required")
	}
	name = strings.TrimSpace(name)
	message = strings.TrimSpace(message)
	if message == "" {
		return ctx, SubagentDefinition{}, "", errors.New("subagent message is required")
	}
	definition, found := m.project.subagents[name]
	if !found {
		return ctx, SubagentDefinition{}, "", fmt.Errorf("subagent definition %q is not available", name)
	}
	if err := m.ensureOpen(); err != nil {
		return ctx, SubagentDefinition{}, "", err
	}
	return ctx, definition, message, nil
}

func (m *subagentManager) startLocked(ctx context.Context, mainAgentSessionID, mainAgentTurnID, message, label string, definition SubagentDefinition, existing []storage.Subagent) (storage.Subagent, error) {
	// The durable store, rather than the in-memory handle map, is the source
	// of truth for the per-main agent quota.
	open := 0
	for _, subagent := range existing {
		if subagent.Status != storage.SubagentStatusClosed {
			open++
		}
	}
	if open >= m.config.maxSubagents {
		return storage.Subagent{}, fmt.Errorf("maximum of %d open subagents reached", m.config.maxSubagents)
	}
	displayName, err := newSubagentDisplayName(existing)
	if err != nil {
		return storage.Subagent{}, err
	}

	id, err := newSubagentID("subagent_")
	if err != nil {
		return storage.Subagent{}, err
	}
	sessionID, err := newSubagentID("session_")
	if err != nil {
		return storage.Subagent{}, err
	}
	turnID, err := newSubagentID("turn_")
	if err != nil {
		return storage.Subagent{}, err
	}
	now := time.Now().UTC()
	record := storage.Subagent{
		ID: id, DisplayName: displayName, Label: label, MainAgentSessionID: mainAgentSessionID, MainAgentTurnID: mainAgentTurnID,
		SubagentSessionID: sessionID, DefinitionName: definition.Name, Provider: definition.Provider, Model: definition.Model,
		Status: storage.SubagentStatusRunning, CurrentSubagentTurnID: turnID, CreatedAt: now, UpdatedAt: now,
	}
	record, err = m.store.Create(ctx, record)
	if err != nil {
		return storage.Subagent{}, err
	}
	rollbackAssignment := m.mainAgent.responseScopes.RegisterAssignmentMetadata(
		mainAgentSessionID,
		mainAgentTurnID,
		toolexecution.ResponseScopePendingResult{
			SubagentID:     id,
			DefinitionName: definition.Name,
			DisplayName:    displayName,
			AssignmentID:   turnID,
			SubagentTurnID: turnID,
		},
	)
	assignmentStarted := false
	defer func() {
		if !assignmentStarted {
			rollbackAssignment()
		}
	}()

	subagent, err := m.createSubagent(definition)
	if err != nil {
		_, _ = m.store.Close(context.Background(), id)
		return storage.Subagent{}, fmt.Errorf("create subagent agent: %w", err)
	}
	instance := &managedSubagent{
		agent: subagent, runs: make(map[string]*agentruntime.Run),
		lastAssignmentMainAgentTurnID: mainAgentTurnID,
		lastAssignmentKey:             subagentMessageIdempotencyKey(mainAgentSessionID, mainAgentTurnID, id, message),
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = subagent.Close()
		_, _ = m.store.Close(context.Background(), id)
		return storage.Subagent{}, ErrClosed
	}
	m.instances[id] = instance
	m.mu.Unlock()

	instance.mu.Lock()
	err = m.startTurnLocked(instance, record, turnID, message)
	instance.mu.Unlock()
	if err != nil {
		m.removeInstance(id)
		_ = subagent.Close()
		_, _ = m.store.Close(context.Background(), id)
		return storage.Subagent{}, err
	}
	assignmentStarted = true
	m.signalChanged()
	return m.getOwned(ctx, mainAgentSessionID, id)
}

func (m *subagentManager) createSubagent(definition SubagentDefinition) (*Agent, error) {
	if m.subagentFactory != nil {
		return m.subagentFactory(definition)
	}
	model, err := m.project.ModelFor(definition.Provider, definition.Model)
	if err != nil {
		return nil, err
	}
	subagentProject := m.project.withSkills(definition.Skills)
	options := []Option{
		withSubagentAgent(),
		withSubagentProject(subagentProject),
		withSharedLangfuse(m.config.langfuse),
		withSharedLogger(m.config.logger),
		WithModel(model),
		withSubagentSystemPrompts(subagentProject, definition),
		WithMessageStorage(m.config.messages),
		WithPermissionStorage(m.config.permissions),
		WithConfirmationStorage(m.config.confirmations),
		WithPermissionPolicy(m.config.permissionPolicy),
		WithPermissionMode(m.mainAgent.PermissionMode()),
		// Interactive permission and confirmation requests are escalated to
		// the main agent session instead of requiring a subagent-session UI.
		WithNonInteractive(m.config.nonInteractive),
		WithToolWorkers(m.config.toolWorkers),
		WithChannelBuffer(m.config.channelBuffer),
		WithSkillReloadPolicy(m.config.skillReload),
	}
	if m.config.compactionModel != nil {
		options = append(options, WithCompactionModel(m.config.compactionModel))
	}
	if m.config.contextEstimator != nil {
		options = append(options, WithContextEstimator(m.config.contextEstimator))
	}
	if m.config.maxProviderSteps > 0 {
		options = append(options, WithProviderStepLimit(m.config.maxProviderSteps))
	}
	for _, tool := range filterSubagentTools(definition, m.config.tools) {
		options = append(options, WithTool(tool))
	}
	return New(m.ctx, options...)
}

func filterSubagentTools(definition SubagentDefinition, tools []toolexecution.Tool) []toolexecution.Tool {
	allowed := make(map[string]struct{}, len(definition.Tools))
	for _, name := range definition.Tools {
		allowed[name] = struct{}{}
	}
	filtered := make([]toolexecution.Tool, 0, len(definition.Tools))
	for _, tool := range tools {
		if _, found := allowed[tool.Definition.Name]; found {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func (project *Project) withSkills(names []string) *Project {
	clone := *project
	available := project.allSkills
	if available == nil {
		available = project.skills
	}
	clone.skills = make(map[string]Skill, len(names))
	for _, name := range names {
		clone.skills[name] = available[name]
	}
	return &clone
}

// withSubagentProject retains the project material a worker needs without
// deciding how that material is presented to the provider. A subagent has no
// manager and consequently no recursive management tools.
func withSubagentProject(project *Project) Option {
	return func(configuration *config) error {
		if project == nil {
			return errors.New("project is required")
		}
		configuration.project = project
		configuration.projectRoot = project.root
		configuration.permissionMode = project.PermissionMode()
		configuration.permissionPolicy.Mode = project.PermissionMode()
		return nil
	}
}

// List returns only records owned by the requested main agent session.
func (m *subagentManager) List(ctx context.Context, mainAgentSessionID string, includeClosed bool) ([]storage.Subagent, error) {
	if strings.TrimSpace(mainAgentSessionID) == "" {
		return nil, errors.New("main agent session ID is required")
	}
	records, err := m.store.ListByMainAgent(nonNilContext(ctx), mainAgentSessionID)
	if err != nil {
		return nil, err
	}
	if includeClosed {
		return records, nil
	}
	filtered := make([]storage.Subagent, 0, len(records))
	for _, record := range records {
		if record.Status != storage.SubagentStatusClosed {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

// Read returns only the latest final assistant answer after a cursor and
// advances the durable observation cursor across all inspected activity. An
// omitted cursor resumes from the subagent's stored cursor instead of replaying
// its full transcript.
func (m *subagentManager) Read(ctx context.Context, mainAgentSessionID, id, afterMessageID string) (subagentReadResult, error) {
	record, err := m.getOwned(nonNilContext(ctx), mainAgentSessionID, id)
	if err != nil {
		return subagentReadResult{}, err
	}
	messages, err := m.mainAgent.ListMessages(nonNilContext(ctx), record.SubagentSessionID)
	if err != nil {
		return subagentReadResult{}, err
	}
	cursor := afterMessageID
	if cursor == "" {
		cursor = record.ObservedMessageID
	}
	start := 0
	if cursor != "" {
		found := false
		for index, message := range messages {
			if message.ID == cursor {
				start, found = index+1, true
				break
			}
		}
		if !found {
			return subagentReadResult{}, fmt.Errorf("subagent message cursor %q was not found", cursor)
		}
	}
	delta := messages[start:]
	result := subagentReadResult{Subagent: record, LastMessageID: cursor}
	for index := len(delta) - 1; index >= 0; index-- {
		message := delta[index]
		if message.Type == agentruntime.MessageTypeAssistant && (record.LastSubagentTurnID == "" || message.TurnID == record.LastSubagentTurnID) {
			answer := storage.CloneMessage(message)
			result.FinalAnswer = &answer
			break
		}
	}
	if len(delta) != 0 {
		last := delta[len(delta)-1]
		observed, observeErr := m.store.Observe(nonNilContext(ctx), id, last.ID, uint64(start+len(delta)))
		if observeErr != nil {
			return subagentReadResult{}, observeErr
		}
		result.Subagent = observed
		result.LastMessageID = last.ID
		m.signalChanged()
	}
	return result, nil
}

// Wait blocks until an owned subagent has unread transcript activity or its
// storage version advances beyond a caller's cursor. Cancelling ctx cancels
// only this wait; it never interrupts the subagent.
func (m *subagentManager) Wait(ctx context.Context, mainAgentSessionID string, ids []string, after map[string]uint64) ([]storage.Subagent, error) {
	ctx = nonNilContext(ctx)
	for _, id := range ids {
		if _, err := m.getOwned(ctx, mainAgentSessionID, id); err != nil {
			return nil, err
		}
	}
	if after == nil {
		// Before taking a state snapshot, unread output is immediately useful
		// to the caller. Once it is all observed, the snapshot makes later
		// close/idle/running transitions wake this wait too.
		unread, err := m.changedSince(ctx, mainAgentSessionID, ids, nil)
		if err != nil || len(unread) != 0 {
			return unread, err
		}
		records, err := m.List(ctx, mainAgentSessionID, true)
		if err != nil {
			return nil, err
		}
		after = make(map[string]uint64, len(records))
		for _, record := range records {
			after[record.ID] = record.Version
		}
	} else {
		cursor := make(map[string]uint64, len(after))
		for id, version := range after {
			cursor[id] = version
		}
		after = cursor
	}
	for {
		changed, err := m.changedSince(ctx, mainAgentSessionID, ids, after)
		if err != nil || len(changed) != 0 {
			return changed, err
		}
		m.mu.RLock()
		notify := m.changed
		closed := m.closed
		m.mu.RUnlock()
		if closed {
			return nil, ErrClosed
		}
		select {
		case <-notify:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// WaitForTurnCompletion joins the turn that is active when this method is
// called. Provider chunks, tool progress, and the delegated user message do
// not complete the wait. If an explicit target is already idle or closed, its
// completed state is returned immediately. With no IDs, all currently running
// subagents become targets.
func (m *subagentManager) WaitForTurnCompletion(ctx context.Context, mainAgentSessionID string, ids []string) ([]storage.Subagent, error) {
	ctx = nonNilContext(ctx)
	records, err := m.List(ctx, mainAgentSessionID, true)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]storage.Subagent, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	if len(ids) == 0 {
		for _, record := range records {
			if record.Status == storage.SubagentStatusRunning {
				ids = append(ids, record.ID)
			}
		}
		if len(ids) == 0 {
			return nil, errors.New("no running subagents to wait for")
		}
	}
	targetTurns := make(map[string]string, len(ids))
	ready := make([]storage.Subagent, 0, len(ids))
	for _, id := range ids {
		record, found := byID[id]
		if !found {
			return nil, storage.ErrSubagentNotFound
		}
		if record.Status != storage.SubagentStatusRunning || record.CurrentSubagentTurnID == "" {
			ready = append(ready, record)
			continue
		}
		targetTurns[id] = record.CurrentSubagentTurnID
	}
	if len(ready) != 0 {
		return ready, nil
	}

	for {
		m.mu.RLock()
		notify := m.changed
		closed := m.closed
		m.mu.RUnlock()
		if closed {
			return nil, ErrClosed
		}
		ready = ready[:0]
		for id, turnID := range targetTurns {
			record, getErr := m.getOwned(ctx, mainAgentSessionID, id)
			if getErr != nil {
				return nil, getErr
			}
			if record.Status == storage.SubagentStatusClosed || record.LastSubagentTurnID == turnID {
				ready = append(ready, record)
			}
		}
		if len(ready) != 0 {
			sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
			return ready, nil
		}
		select {
		case <-notify:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (m *subagentManager) changedSince(ctx context.Context, mainAgentSessionID string, ids []string, after map[string]uint64) ([]storage.Subagent, error) {
	records, err := m.List(ctx, mainAgentSessionID, true)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	changed := make([]storage.Subagent, 0)
	for _, record := range records {
		if len(wanted) != 0 {
			if _, ok := wanted[record.ID]; !ok {
				continue
			}
		}
		if after != nil && record.Version > after[record.ID] {
			changed = append(changed, record)
			continue
		}
		messages, listErr := m.mainAgent.ListMessages(ctx, record.SubagentSessionID)
		if listErr != nil {
			return nil, listErr
		}
		if len(messages) != 0 && messages[len(messages)-1].ID != record.ObservedMessageID {
			changed = append(changed, record)
		}
	}
	return changed, nil
}

// Send delivers direct application/UI work without main agent-turn deduplication.
// Model-facing calls use SendFromMainAgentTurn so retries cannot multiply work.
func (m *subagentManager) Send(ctx context.Context, mainAgentSessionID, id, content string) (storage.Subagent, error) {
	ctx = nonNilContext(ctx)
	content = normalizeSubagentMessage(content)
	if content == "" {
		return storage.Subagent{}, errors.New("subagent message is required")
	}
	record, err := m.getOwned(ctx, mainAgentSessionID, id)
	if err != nil {
		return storage.Subagent{}, err
	}
	if record.Status == storage.SubagentStatusClosed {
		return storage.Subagent{}, storage.ErrSubagentClosed
	}
	instance, err := m.instance(id)
	if err != nil {
		return storage.Subagent{}, err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	// Refresh under the per-subagent turn gate so a monitor cannot race a
	// completion into an accidental concurrent turn.
	record, err = m.getOwned(ctx, mainAgentSessionID, id)
	if err != nil {
		return storage.Subagent{}, err
	}
	if record.Status == storage.SubagentStatusClosed {
		return storage.Subagent{}, storage.ErrSubagentClosed
	}
	if err := m.validateSubagentSend(ctx, record); err != nil {
		return storage.Subagent{}, err
	}
	return m.sendLocked(ctx, instance, record, content)
}

// StatusFromMainAgentTurn returns at most one fresh lifecycle snapshot for a
// subagent in a main agent turn. Later calls in the same turn receive the original
// snapshot so model-facing status checks cannot become polling.
func (m *subagentManager) StatusFromMainAgentTurn(ctx context.Context, mainAgentSessionID, mainAgentTurnID, id string) (toolexecution.SubagentStatusSnapshot, error) {
	ctx = nonNilContext(ctx)
	mainAgentTurnID = strings.TrimSpace(mainAgentTurnID)
	if mainAgentTurnID == "" {
		return toolexecution.SubagentStatusSnapshot{}, errors.New("main agent turn ID is required")
	}
	if _, err := m.getOwned(ctx, mainAgentSessionID, id); err != nil {
		return toolexecution.SubagentStatusSnapshot{}, err
	}
	instance, err := m.instance(id)
	if err != nil {
		return toolexecution.SubagentStatusSnapshot{}, err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.lastStatusMainAgentTurnID == mainAgentTurnID {
		return toolexecution.SubagentStatusSnapshot{Subagent: storage.CloneSubagent(instance.lastStatusSnapshot), Repeated: true}, nil
	}
	record, err := m.getOwned(ctx, mainAgentSessionID, id)
	if err != nil {
		return toolexecution.SubagentStatusSnapshot{}, err
	}
	instance.lastStatusMainAgentTurnID = mainAgentTurnID
	instance.lastStatusSnapshot = storage.CloneSubagent(record)
	return toolexecution.SubagentStatusSnapshot{Subagent: record}, nil
}

// SendFromMainAgentTurn accepts at most one assignment from a main agent turn to one
// subagent. Exact retries return duplicate; changed retries return already_sent,
// and both decisions precede lifecycle admission. A pending authoritative
// result, including any running subagent turn, is also a controlled non-error
// result so the model can wait without chasing or queuing work. None of these
// cases adds work.
func (m *subagentManager) SendFromMainAgentTurn(ctx context.Context, mainAgentSessionID, mainAgentTurnID, id, content string) (toolexecution.SubagentSendResult, error) {
	ctx = nonNilContext(ctx)
	mainAgentTurnID = strings.TrimSpace(mainAgentTurnID)
	content = normalizeSubagentMessage(content)
	if mainAgentTurnID == "" {
		return toolexecution.SubagentSendResult{}, errors.New("main agent turn ID is required")
	}
	if content == "" {
		return toolexecution.SubagentSendResult{}, errors.New("subagent message is required")
	}
	record, err := m.getOwned(ctx, mainAgentSessionID, id)
	if err != nil {
		return toolexecution.SubagentSendResult{}, err
	}
	instance, err := m.instance(id)
	if err != nil {
		return toolexecution.SubagentSendResult{}, err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	record, err = m.getOwned(ctx, mainAgentSessionID, id)
	if err != nil {
		return toolexecution.SubagentSendResult{}, err
	}
	key := subagentMessageIdempotencyKey(mainAgentSessionID, mainAgentTurnID, id, content)
	if instance.lastAssignmentMainAgentTurnID == mainAgentTurnID {
		action := toolexecution.SubagentSendAlreadySent
		deduplicated := false
		if instance.lastAssignmentKey == key {
			action = toolexecution.SubagentSendDuplicate
			deduplicated = true
		}
		return toolexecution.SubagentSendResult{
			Action: action, Subagent: record, IdempotencyKey: key,
			Deduplicated: deduplicated, Accepted: false,
		}, nil
	}
	if record.Status == storage.SubagentStatusClosed {
		return toolexecution.SubagentSendResult{}, storage.ErrSubagentClosed
	}
	if record.Status == storage.SubagentStatusRunning {
		return toolexecution.SubagentSendResult{
			Action: toolexecution.SubagentSendResultPending, Subagent: record,
			IdempotencyKey: key, Accepted: false,
		}, nil
	}
	if err := m.validateSubagentSend(ctx, record); err != nil {
		if errors.Is(err, storage.ErrSubagentResultPending) {
			return toolexecution.SubagentSendResult{
				Action: toolexecution.SubagentSendResultPending, Subagent: record,
				IdempotencyKey: key, Accepted: false,
			}, nil
		}
		if errors.Is(err, storage.ErrSubagentCompleted) {
			return toolexecution.SubagentSendResult{
				Action: toolexecution.SubagentSendCompleted, Subagent: record,
				IdempotencyKey: key, Accepted: false,
			}, nil
		}
		return toolexecution.SubagentSendResult{}, err
	}
	rollbackRecovery := func() {}
	if record.LastResultStatus == storage.SubagentResultFailed {
		allowed, rollback := m.mainAgent.responseScopes.ReserveFailedRecovery(
			mainAgentSessionID,
			mainAgentTurnID,
			record.ID,
			subagentFailureFingerprint(record.LastResultError),
		)
		if !allowed {
			return toolexecution.SubagentSendResult{
				Action: toolexecution.SubagentSendRecoveryExhausted, Subagent: record,
				IdempotencyKey: key, Accepted: false,
			}, nil
		}
		rollbackRecovery = rollback
	}
	action := toolexecution.SubagentSendStarted
	if record.Status == storage.SubagentStatusRunning {
		action = toolexecution.SubagentSendQueued
	}
	rollbackAssignment := m.mainAgent.responseScopes.RegisterAssignmentMetadata(
		mainAgentSessionID,
		mainAgentTurnID,
		toolexecution.ResponseScopePendingResult{
			SubagentID:     id,
			DefinitionName: record.DefinitionName,
			DisplayName:    record.DisplayName,
			AssignmentID:   key,
		},
	)
	updated, err := m.sendLocked(ctx, instance, record, content)
	if err != nil {
		rollbackAssignment()
		rollbackRecovery()
		return toolexecution.SubagentSendResult{}, err
	}
	instance.lastAssignmentMainAgentTurnID = mainAgentTurnID
	instance.lastAssignmentKey = key
	return toolexecution.SubagentSendResult{
		Action: action, Subagent: updated, IdempotencyKey: key,
		Accepted: true,
	}, nil
}

// sendLocked starts immediately when idle and appends FIFO mailbox work when
// a turn is already active. The caller holds instance.mu.
func (m *subagentManager) sendLocked(ctx context.Context, instance *managedSubagent, record storage.Subagent, content string) (storage.Subagent, error) {
	if record.Status == storage.SubagentStatusRunning {
		messageID, idErr := newSubagentID("submsg_")
		if idErr != nil {
			return storage.Subagent{}, idErr
		}
		queued, queueErr := m.store.Enqueue(ctx, record.ID, storage.SubagentQueuedMessage{ID: messageID, Content: content, CreatedAt: time.Now().UTC()})
		if queueErr == nil {
			m.signalChanged()
		}
		return queued, queueErr
	}
	turnID, err := newSubagentID("turn_")
	if err != nil {
		return storage.Subagent{}, err
	}
	updated, err := m.transition(ctx, record.ID, storage.SubagentStatusRunning, turnID, "", "", "", "", "")
	if err != nil {
		return storage.Subagent{}, err
	}
	if err := m.startTurnLocked(instance, updated, turnID, content); err != nil {
		_, _ = m.transition(context.Background(), record.ID, storage.SubagentStatusIdle, "", turnID, err.Error(), storage.SubagentResultFailed, "", "")
		return storage.Subagent{}, err
	}
	m.signalChanged()
	return m.getOwned(ctx, record.MainAgentSessionID, record.ID)
}

// validateSubagentSend allows running work to accept ordered mailbox input.
// An idle incomplete or failed subagent may resume only after the main agent consumed
// that result's result. Completed subagents are never reused.
func (m *subagentManager) validateSubagentSend(ctx context.Context, record storage.Subagent) error {
	if record.Status == storage.SubagentStatusRunning {
		return nil
	}
	if record.Status == storage.SubagentStatusClosed {
		return storage.ErrSubagentClosed
	}
	switch record.LastResultStatus {
	case storage.SubagentResultIncomplete, storage.SubagentResultFailed:
		return m.validateLatestSubagentResultObserved(ctx, record)
	case storage.SubagentResultCompleted:
		if err := m.validateLatestSubagentResultObserved(ctx, record); err != nil {
			return err
		}
		return storage.ErrSubagentCompleted
	default:
		return storage.ErrSubagentReportUnavailable
	}
}

func normalizeSubagentMessage(content string) string {
	return strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
}

func subagentFailureFingerprint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var normalized strings.Builder
	inDigits := false
	for _, character := range value {
		if character >= '0' && character <= '9' {
			if !inDigits {
				normalized.WriteByte('#')
				inDigits = true
			}
			continue
		}
		inDigits = false
		normalized.WriteRune(character)
	}
	fingerprint := strings.Join(strings.Fields(normalized.String()), " ")
	if fingerprint == "" {
		return "failed"
	}
	return fingerprint
}

func subagentMessageIdempotencyKey(mainAgentSessionID, mainAgentTurnID, subagentID, content string) string {
	payload := strings.Join([]string{
		"subagent-message-v1", mainAgentSessionID, mainAgentTurnID, subagentID,
		normalizeSubagentMessage(content),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// Interrupt stops only the current subagent turn. The subagent instance remains
// idle and can accept another Send after the terminal event is recorded.
func (m *subagentManager) Interrupt(ctx context.Context, mainAgentSessionID, id, reason string) error {
	if _, err := m.getOwned(nonNilContext(ctx), mainAgentSessionID, id); err != nil {
		return err
	}
	instance, err := m.instance(id)
	if err != nil {
		return err
	}
	instance.mu.Lock()
	run := instance.run
	subagent := instance.agent
	instance.mu.Unlock()
	if subagent == nil || run == nil || run.Done() {
		return nil
	}
	return run.Interrupt(nonNilContext(ctx), reason)
}

// watchMainAgentRun propagates an interrupted main agent turn only to subagents that
// were created by that exact tool chain. It intentionally does not react to a
// normal main agent completion: delegated work may outlive the tool invocation.
func (m *subagentManager) watchMainAgentRun(run *agentruntime.Run) {
	if run == nil {
		return
	}
	go func() {
		if mainAgentRunInterrupted(run.Events()) {
			m.interruptMainAgentTurn(run.SessionID(), run.TurnID())
			return
		}
		subscription := run.Subscribe(m.ctx)
		for event := range subscription.Events {
			if event.Type == agentruntime.AgentInterrupted {
				m.interruptMainAgentTurn(run.SessionID(), run.TurnID())
				return
			}
		}
		// A fast interruption can happen between the retained snapshot and
		// subscription registration; the final retained check closes that gap.
		if mainAgentRunInterrupted(run.Events()) {
			m.interruptMainAgentTurn(run.SessionID(), run.TurnID())
		}
	}()
}

func mainAgentRunInterrupted(events []agentruntime.AgentEvent) bool {
	for _, event := range events {
		if event.Type == agentruntime.AgentInterrupted {
			return true
		}
	}
	return false
}

func (m *subagentManager) interruptMainAgentTurn(mainAgentSessionID, mainAgentTurnID string) {
	records, err := m.store.ListByMainAgent(context.Background(), mainAgentSessionID)
	if err != nil {
		return
	}
	for _, record := range records {
		if record.MainAgentTurnID != mainAgentTurnID || record.Status != storage.SubagentStatusRunning {
			continue
		}
		// Use the run directly so this internal lifecycle propagation cannot be
		// rejected by a caller cancellation context.
		instance, instanceErr := m.instance(record.ID)
		if instanceErr != nil {
			continue
		}
		instance.mu.Lock()
		run := instance.run
		instance.mu.Unlock()
		if run != nil && !run.Done() {
			_ = run.Interrupt(context.Background(), "main agent turn interrupted")
		}
	}
}

// autoCloseScopeSubagents releases completed and failed subagents touched by a
// quiescent response scope. Incomplete subagents stay open for future follow-up.
func (m *subagentManager) autoCloseScopeSubagents(ctx context.Context, mainAgentSessionID, scopeID string, ids []string) {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	for _, id := range ids {
		closed, ok := m.autoCloseScopeSubagent(nonNilContext(ctx), mainAgentSessionID, scopeID, id)
		if ok {
			m.recordAutoClosedSubagent(closed)
		}
	}
}

func (m *subagentManager) autoCloseScopeSubagent(ctx context.Context, mainAgentSessionID, scopeID, id string) (storage.Subagent, bool) {
	ctx = nonNilContext(ctx)
	record, err := m.getOwned(ctx, mainAgentSessionID, id)
	if err != nil {
		return storage.Subagent{}, false
	}
	instance, instanceErr := m.instance(id)
	if instanceErr != nil {
		if record.Status != storage.SubagentStatusIdle || len(record.Pending) != 0 {
			return storage.Subagent{}, false
		}
		if err := m.validateSubagentClose(ctx, record); err != nil {
			return storage.Subagent{}, false
		}
		if !m.mainAgent.responseScopes.SubagentExclusiveToScope(mainAgentSessionID, scopeID, id) {
			return storage.Subagent{}, false
		}
		closed, closeErr := m.store.Close(ctx, id)
		if closeErr == nil {
			m.signalChanged()
			m.publishSystemEvent(SystemEvent{
				Type: SystemSubagentClosed, MainAgentSessionID: mainAgentSessionID, MainAgentTurnID: scopeID,
				SubagentClosed: &SubagentClosedEvent{
					Subagent: closed, PreviousStatus: record.Status,
					PreviousResultStatus: record.LastResultStatus, Automatic: true,
				},
			})
			return closed, true
		}
		return storage.Subagent{}, false
	}
	instance.mu.Lock()
	record, err = m.getOwned(ctx, mainAgentSessionID, id)
	if err != nil {
		instance.mu.Unlock()
		return storage.Subagent{}, false
	}
	if record.Status != storage.SubagentStatusIdle || len(record.Pending) != 0 {
		instance.mu.Unlock()
		return storage.Subagent{}, false
	}
	if err := m.validateSubagentClose(ctx, record); err != nil {
		instance.mu.Unlock()
		return storage.Subagent{}, false
	}
	if !m.mainAgent.responseScopes.SubagentExclusiveToScope(mainAgentSessionID, scopeID, id) {
		instance.mu.Unlock()
		return storage.Subagent{}, false
	}
	closed, err := m.store.Close(ctx, id)
	if err != nil {
		instance.mu.Unlock()
		return storage.Subagent{}, false
	}
	subagent := instance.agent
	instance.agent = nil
	instance.mu.Unlock()
	if subagent != nil {
		_ = subagent.Close()
	}
	m.signalChanged()
	m.publishSystemEvent(SystemEvent{
		Type: SystemSubagentClosed, MainAgentSessionID: mainAgentSessionID, MainAgentTurnID: scopeID,
		SubagentClosed: &SubagentClosedEvent{
			Subagent: closed, PreviousStatus: record.Status,
			PreviousResultStatus: record.LastResultStatus, Automatic: true,
		},
	})
	return closed, true
}

// CloseSubagent destructively closes one owned subagent for an application-owned
// caller. Automatic lifecycle cleanup uses autoCloseScopeSubagents instead.
func (m *subagentManager) CloseSubagent(ctx context.Context, mainAgentSessionID, id string) (toolexecution.SubagentCloseResult, error) {
	ctx = nonNilContext(ctx)
	record, err := m.getOwned(ctx, mainAgentSessionID, id)
	if err != nil {
		return toolexecution.SubagentCloseResult{}, err
	}
	if record.Status == storage.SubagentStatusClosed {
		return toolexecution.SubagentCloseResult{}, storage.ErrSubagentClosed
	}
	instance, instanceErr := m.instance(id)
	if instanceErr != nil {
		if record.Status == storage.SubagentStatusRunning {
			return toolexecution.SubagentCloseResult{}, fmt.Errorf("close running subagent: runtime instance unavailable: %w", instanceErr)
		}
		closed, closeErr := m.store.Close(ctx, id)
		if closeErr != nil {
			return toolexecution.SubagentCloseResult{}, closeErr
		}
		m.signalChanged()
		result := toolexecution.SubagentCloseResult{
			Subagent: closed, PreviousStatus: record.Status, PreviousResultStatus: record.LastResultStatus,
			DroppedMessages: len(record.Pending),
		}
		m.mainAgent.responseScopes.CancelSubagentAssignments(mainAgentSessionID, id)
		m.publishSystemEvent(SystemEvent{
			Type: SystemSubagentClosed, MainAgentSessionID: mainAgentSessionID,
			SubagentClosed: &SubagentClosedEvent{
				Subagent: closed, PreviousStatus: result.PreviousStatus,
				PreviousResultStatus: result.PreviousResultStatus, DroppedMessages: result.DroppedMessages,
			},
		})
		return result, nil
	}

	instance.mu.Lock()
	record, err = m.getOwned(ctx, mainAgentSessionID, id)
	if err != nil {
		instance.mu.Unlock()
		return toolexecution.SubagentCloseResult{}, err
	}
	if record.Status == storage.SubagentStatusClosed {
		instance.mu.Unlock()
		return toolexecution.SubagentCloseResult{}, storage.ErrSubagentClosed
	}
	run := instance.run
	subagent := instance.agent
	closed, err := m.store.Close(ctx, id)
	if err != nil {
		instance.mu.Unlock()
		return toolexecution.SubagentCloseResult{}, err
	}
	instance.agent = nil
	instance.mu.Unlock()

	interrupted := record.Status == storage.SubagentStatusRunning && run != nil && !run.Done()
	if interrupted {
		_ = run.Interrupt(context.Background(), "subagent closed by explicit user request")
	}
	if subagent != nil {
		_ = subagent.Close()
	}
	m.signalChanged()
	result := toolexecution.SubagentCloseResult{
		Subagent: closed, PreviousStatus: record.Status, PreviousResultStatus: record.LastResultStatus,
		DroppedMessages: len(record.Pending), Interrupted: interrupted,
	}
	m.mainAgent.responseScopes.CancelSubagentAssignments(mainAgentSessionID, id)
	m.publishSystemEvent(SystemEvent{
		Type: SystemSubagentClosed, MainAgentSessionID: mainAgentSessionID,
		SubagentClosed: &SubagentClosedEvent{
			Subagent: closed, PreviousStatus: result.PreviousStatus,
			PreviousResultStatus: result.PreviousResultStatus, DroppedMessages: result.DroppedMessages,
			Interrupted: result.Interrupted,
		},
	})
	return result, nil
}

// validateSubagentClose makes close a cleanup-only operation. An idle state
// alone is insufficient: incomplete work must remain available for follow-up,
// while completed and failed results must reach a main agent result consumer
// before the subagent can disappear from the active set.
func (m *subagentManager) validateSubagentClose(ctx context.Context, record storage.Subagent) error {
	switch record.LastResultStatus {
	case storage.SubagentResultCompleted, storage.SubagentResultFailed:
		return m.validateLatestSubagentResultObserved(ctx, record)
	case storage.SubagentResultIncomplete:
		return storage.ErrSubagentIncomplete
	default:
		return storage.ErrSubagentReportUnavailable
	}
}

func (m *subagentManager) validateLatestSubagentResultObserved(ctx context.Context, record storage.Subagent) error {
	messages, err := m.mainAgent.ListMessages(nonNilContext(ctx), record.SubagentSessionID)
	if err != nil {
		return fmt.Errorf("read subagent result cursor: %w", err)
	}
	if len(messages) == 0 {
		return storage.ErrSubagentResultPending
	}
	latest := messages[len(messages)-1]
	if record.ObservedMessageID != latest.ID || record.ObservedVersion < uint64(len(messages)) {
		return storage.ErrSubagentResultPending
	}
	return nil
}

// Run returns the retained subagent run for nested SSE backfill and live
// subscription. It deliberately remains main agent-ownership checked.
func (m *subagentManager) Run(ctx context.Context, mainAgentSessionID, id, turnID string) (*agentruntime.Run, error) {
	if _, err := m.getOwned(nonNilContext(ctx), mainAgentSessionID, id); err != nil {
		return nil, err
	}
	instance, err := m.instance(id)
	if err != nil {
		return nil, err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	run := instance.runs[turnID]
	if run == nil {
		return nil, agentruntime.ErrRunNotFound
	}
	return run, nil
}

func (m *subagentManager) startTurnLocked(instance *managedSubagent, record storage.Subagent, turnID, content string) error {
	if err := m.ensureOpen(); err != nil {
		return err
	}
	if instance.agent == nil {
		return storage.ErrSubagentClosed
	}
	run, subscription, err := instance.agent.StartSubscribed(m.ctx, agentruntime.Request{
		SessionID: record.SubagentSessionID, TurnID: turnID,
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: content},
	})
	if err != nil {
		return fmt.Errorf("start subagent turn: %w", err)
	}
	// Runtime.StartSubscribed fences RunStarted, not the subsequent append of
	// its initial user message. A manager Start has a stronger, useful
	// contract: once it returns, Read can immediately render the delegated
	// input. Wait only for that local storage commit; provider completion is
	// deliberately not part of this boundary.
	if err := m.waitForInitialInput(record.SubagentSessionID, turnID, run); err != nil {
		_ = run.Interrupt(context.Background(), "initial subagent input was not committed")
		return err
	}
	instance.run = run
	instance.runs[run.TurnID()] = run
	go m.monitor(record.ID, instance, run, subscription)
	return nil
}

func (m *subagentManager) waitForInitialInput(sessionID, turnID string, run *agentruntime.Run) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		messages, err := m.config.messages.List(m.ctx, sessionID)
		if err != nil {
			return fmt.Errorf("observe initial subagent input: %w", err)
		}
		for _, message := range messages {
			if message.TurnID == turnID && message.Type == agentruntime.MessageTypeUser {
				return nil
			}
		}
		if run.Done() {
			if _, err := run.Result(); err != nil {
				return fmt.Errorf("initial subagent input was not committed: %w", err)
			}
			return errors.New("initial subagent input was not committed")
		}
		select {
		case <-m.ctx.Done():
			return ErrClosed
		case <-deadline.C:
			return errors.New("timed out waiting for initial subagent input")
		case <-ticker.C:
		}
	}
}

func (m *subagentManager) monitor(id string, instance *managedSubagent, run *agentruntime.Run, subscription agentruntime.EventSubscription) {
	for event := range subscription.Events {
		if permissionEvent, ok := m.subagentPermissionEvent(id, event); ok {
			m.publishPermission(permissionEvent)
		}
		if confirmationEvent, ok := m.subagentConfirmationEvent(id, event); ok {
			m.publishConfirmation(confirmationEvent)
		}
		m.signalChanged()
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.run != run {
		return
	}
	record, found, err := m.store.Get(context.Background(), id)
	if err != nil || !found || record.Status == storage.SubagentStatusClosed {
		return
	}
	var lastTurnError string
	if _, runErr := run.Result(); runErr != nil {
		lastTurnError = runErr.Error()
	}
	messages, messagesErr := m.config.messages.List(context.Background(), record.SubagentSessionID)
	if messagesErr != nil && lastTurnError == "" {
		lastTurnError = "read completed subagent output: " + messagesErr.Error()
	}
	lastResultStatus := storage.SubagentResultIncomplete
	lastSummary := "Subagent turn ended without a successful result report."
	lastNextStep := "Review the final answer and send one focused follow-up if required."
	if run.StepLimitFinalized() {
		lastSummary = "Subagent reached its provider-step limit and returned a text-only final summary."
		lastNextStep = "Review the summary and send one focused follow-up for any remaining work."
	} else if run.CompletionRepairCount() > 0 {
		lastSummary = "Subagent turn ended without a successful result report after bounded repair attempts."
	}
	if lastTurnError != "" {
		lastResultStatus = storage.SubagentResultFailed
		lastSummary = ""
		lastNextStep = ""
	} else if reported, ok := reportedSubagentReport(run.TurnID(), messages); ok {
		lastSummary = reported.Summary
		lastNextStep = reported.NextStep
		switch reported.Status {
		case toolexecution.SubagentReportCompleted:
			lastResultStatus = storage.SubagentResultCompleted
			lastNextStep = ""
		case toolexecution.SubagentReportFailed:
			lastResultStatus = storage.SubagentResultFailed
			lastTurnError = reported.Error
			lastNextStep = ""
		}
	}
	completed, err := m.transition(context.Background(), id, storage.SubagentStatusIdle, "", run.TurnID(), lastTurnError, lastResultStatus, lastSummary, lastNextStep)
	if err != nil {
		return
	}
	instance.run = nil
	m.signalChanged()
	result := subagentResultFromMessages(completed, messages)
	m.publishResult(result)
	// One completion owns the dequeue/start transition, so mailbox order is
	// preserved even when Send races completion.
	afterDequeue, next, err := m.store.Dequeue(context.Background(), id)
	if err != nil || next == nil {
		return
	}
	turnID, err := newSubagentID("turn_")
	if err != nil {
		return
	}
	_ = afterDequeue
	running, err := m.transition(context.Background(), id, storage.SubagentStatusRunning, turnID, "", "", "", "", "")
	if err != nil {
		return
	}
	if err := m.startTurnLocked(instance, running, turnID, next.Content); err != nil {
		_, _ = m.transition(context.Background(), id, storage.SubagentStatusIdle, "", turnID, err.Error(), storage.SubagentResultFailed, "", "")
	}
	m.signalChanged()
}

func (m *subagentManager) setPermissionMode(ctx context.Context, mode permission.Mode) error {
	m.mu.RLock()
	instances := make([]*managedSubagent, 0, len(m.instances))
	for _, instance := range m.instances {
		instances = append(instances, instance)
	}
	m.mu.RUnlock()
	var first error
	for _, instance := range instances {
		instance.mu.Lock()
		subagent := instance.agent
		instance.mu.Unlock()
		if subagent != nil {
			if err := subagent.SetPermissionMode(nonNilContext(ctx), mode); err != nil && !errors.Is(err, ErrClosed) && first == nil {
				first = err
			}
		}
	}
	return first
}

// transition retries optimistic updates that race a Read observation. The
// lifecycle decision is still serialized by the subagent instance lock; this
// retry solely accommodates the intentionally independent read cursor.
func (m *subagentManager) transition(ctx context.Context, id string, status storage.SubagentStatus, currentTurnID, lastTurnID, lastTurnError string, lastResultStatus storage.SubagentResultStatus, lastTurnSummary, lastTurnNextStep string) (storage.Subagent, error) {
	for attempts := 0; attempts < 8; attempts++ {
		record, found, err := m.store.Get(ctx, id)
		if err != nil {
			return storage.Subagent{}, err
		}
		if !found {
			return storage.Subagent{}, storage.ErrSubagentNotFound
		}
		if record.Status == storage.SubagentStatusClosed {
			return storage.Subagent{}, storage.ErrSubagentClosed
		}
		last := lastTurnID
		turnError := lastTurnError
		resultStatus := lastResultStatus
		summary := lastTurnSummary
		nextStep := lastTurnNextStep
		if last == "" {
			last = record.LastSubagentTurnID
			turnError = record.LastResultError
			resultStatus = record.LastResultStatus
			summary = record.LastResultSummary
			nextStep = record.LastResultNextStep
		}
		updated, err := m.store.Update(ctx, id, record.Version, storage.SubagentUpdate{
			Status: status, CurrentSubagentTurnID: currentTurnID, LastSubagentTurnID: last, LastResultError: turnError,
			LastResultStatus: resultStatus, LastResultSummary: summary, LastResultNextStep: nextStep,
		})
		if !errors.Is(err, storage.ErrSubagentVersionConflict) {
			return updated, err
		}
	}
	return storage.Subagent{}, storage.ErrSubagentVersionConflict
}

// Close closes every live subagent before the main-agent executor is cancelled. This
// order prevents a main agent tool wait from being stranded on a subagent executor.
func (m *subagentManager) Close() error {
	m.closeResults()
	m.closeConfirmations()
	m.closePermissions()
	m.closeSystemEvents()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	instances := make(map[string]*managedSubagent, len(m.instances))
	for id, instance := range m.instances {
		instances[id] = instance
	}
	m.mu.Unlock()
	var first error
	for id, instance := range instances {
		if _, err := m.store.Close(context.Background(), id); err != nil && !errors.Is(err, storage.ErrSubagentNotFound) && first == nil {
			first = err
		}
		if subagent := instance.releaseAgent(); subagent != nil {
			if err := subagent.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	m.signalChanged()
	return first
}

func (m *subagentManager) getOwned(ctx context.Context, mainAgentSessionID, id string) (storage.Subagent, error) {
	if strings.TrimSpace(mainAgentSessionID) == "" || strings.TrimSpace(id) == "" {
		return storage.Subagent{}, storage.ErrSubagentNotFound
	}
	record, found, err := m.store.Get(ctx, id)
	if err != nil {
		return storage.Subagent{}, err
	}
	if !found || record.MainAgentSessionID != mainAgentSessionID {
		return storage.Subagent{}, storage.ErrSubagentNotFound
	}
	return record, nil
}

func (m *subagentManager) instance(id string) (*managedSubagent, error) {
	m.mu.RLock()
	instance := m.instances[id]
	m.mu.RUnlock()
	if instance == nil {
		return nil, storage.ErrSubagentNotFound
	}
	return instance, nil
}

func (m *subagentManager) removeInstance(id string) {
	m.mu.Lock()
	delete(m.instances, id)
	m.mu.Unlock()
}

func (instance *managedSubagent) releaseAgent() *Agent {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	subagent := instance.agent
	instance.agent = nil
	return subagent
}

func (m *subagentManager) ensureOpen() error {
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	return nil
}

func (m *subagentManager) signalChanged() {
	m.mu.Lock()
	close(m.changed)
	m.changed = make(chan struct{})
	m.mu.Unlock()
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func newSubagentID(prefix string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate subagent identifier: %w", err)
	}
	return prefix + hex.EncodeToString(bytes), nil
}

var subagentDisplayNames = []string{
	"Aster", "Atlas", "Cedar", "Cleo", "Echo", "Ember", "Fern", "Iris",
	"Juno", "Kai", "Lark", "Luna", "Mira", "Nova", "Onyx", "Orion",
	"Piper", "Quinn", "Remy", "River", "Robin", "Sage", "Sol", "Tali",
	"Theo", "Vale", "Vega", "Wren", "Yara", "Zeno", "Zinnia", "Zora",
}

// newSubagentDisplayName assigns a short session-local name that users can
// comfortably reference. A random starting point preserves variety while the
// linear scan guarantees uniqueness among retained sibling records.
func newSubagentDisplayName(existing []storage.Subagent) (string, error) {
	used := make(map[string]struct{}, len(existing))
	for _, record := range existing {
		if record.DisplayName != "" {
			used[strings.ToLower(record.DisplayName)] = struct{}{}
		}
	}
	var random [1]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate subagent display name: %w", err)
	}
	start := int(random[0]) % len(subagentDisplayNames)
	for offset := range subagentDisplayNames {
		candidate := subagentDisplayNames[(start+offset)%len(subagentDisplayNames)]
		if _, found := used[strings.ToLower(candidate)]; !found {
			return candidate, nil
		}
	}
	suffix, err := newSubagentID("")
	if err != nil {
		return "", err
	}
	return "Agent-" + suffix[:6], nil
}
