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
// layers are thin adapters over this owner of child runtimes and the durable
// parent/child relationship records.
type subagentManager struct {
	parent  *Agent
	store   storage.SubagentStorage
	project *Project
	config  config
	ctx     context.Context

	mu           sync.RWMutex
	startMu      sync.Mutex // makes the per-parent open-instance quota atomic.
	instances    map[string]*managedSubagent
	closed       bool
	changed      chan struct{}
	childFactory func(SubagentDefinition) (*Agent, error)

	callbackMu             sync.Mutex
	nextCallbackSubscriber uint64
	callbackSubscribers    map[uint64]*subagentCallbackSubscriber
	callbacksClosed        bool
}

type managedSubagent struct {
	// agent is cleared as soon as a child closes. runs intentionally remains:
	// completed event history is transport state, not a live child runtime.
	agent *Agent

	mu                       sync.Mutex // serializes the one active child turn and its mailbox.
	run                      *agentruntime.Run
	runs                     map[string]*agentruntime.Run
	lastDispatchParentTurnID string
	lastDispatchKey          string
	lastStatusParentTurnID   string
	lastStatusSnapshot       storage.Subagent
}

type subagentReadResult = SubagentReadResult

func newSubagentManager(parent *Agent, configuration config) (*subagentManager, error) {
	if parent == nil || configuration.project == nil || configuration.subagents == nil {
		return nil, errors.New("subagent manager requires parent, project, and storage")
	}
	if configuration.maxSubagents <= 0 {
		return nil, errors.New("subagent maximum must be positive")
	}
	return &subagentManager{
		parent: parent, store: configuration.subagents, project: configuration.project,
		config: configuration, ctx: parent.context, instances: make(map[string]*managedSubagent),
		changed: make(chan struct{}), callbackSubscribers: make(map[uint64]*subagentCallbackSubscriber),
	}, nil
}

// Start creates a separately-addressable child session and begins its first
// turn without waiting for provider completion.
func (m *subagentManager) Start(ctx context.Context, parentSessionID, parentTurnID, name, message, label string) (storage.Subagent, error) {
	ctx, definition, message, err := m.prepareStart(ctx, parentSessionID, parentTurnID, name, message)
	if err != nil {
		return storage.Subagent{}, err
	}
	m.startMu.Lock()
	defer m.startMu.Unlock()
	existing, err := m.store.ListByParent(ctx, parentSessionID)
	if err != nil {
		return storage.Subagent{}, err
	}
	return m.startLocked(ctx, parentSessionID, parentTurnID, message, label, definition, existing)
}

// StartOrReuse provides conversational routing for the model-facing
// start_subagent tool. Direct callers use Start when they explicitly want a
// new child. With implicit routing, one open child is reused and multiple open
// children are returned for user selection.
func (m *subagentManager) StartOrReuse(ctx context.Context, parentSessionID, parentTurnID, name, message, label string, newInstance bool) (toolexecution.SubagentStartResult, error) {
	ctx, definition, message, err := m.prepareStart(ctx, parentSessionID, parentTurnID, name, message)
	if err != nil {
		return toolexecution.SubagentStartResult{}, err
	}
	m.startMu.Lock()
	defer m.startMu.Unlock()
	existing, err := m.store.ListByParent(ctx, parentSessionID)
	if err != nil {
		return toolexecution.SubagentStartResult{}, err
	}
	open := make([]storage.Subagent, 0, len(existing))
	for _, child := range existing {
		if child.Status != storage.SubagentStatusClosed {
			open = append(open, child)
		}
	}
	if !newInstance {
		if len(open) == 1 {
			dispatched, sendErr := m.SendFromParentTurn(ctx, parentSessionID, parentTurnID, open[0].ID, message)
			if sendErr != nil {
				return toolexecution.SubagentStartResult{}, sendErr
			}
			return toolexecution.SubagentStartResult{Action: toolexecution.SubagentStartReused, DispatchAction: dispatched.Action, Subagent: dispatched.Subagent}, nil
		}
		if len(open) > 1 {
			return toolexecution.SubagentStartResult{Action: toolexecution.SubagentStartSelectionRequired, Candidates: open}, nil
		}
	}
	record, err := m.startLocked(ctx, parentSessionID, parentTurnID, message, label, definition, existing)
	if err != nil {
		return toolexecution.SubagentStartResult{}, err
	}
	return toolexecution.SubagentStartResult{Action: toolexecution.SubagentStartCreated, Subagent: record}, nil
}

func (m *subagentManager) prepareStart(ctx context.Context, parentSessionID, parentTurnID, name, message string) (context.Context, SubagentDefinition, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ctx, SubagentDefinition{}, "", err
	}
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(parentTurnID) == "" {
		return ctx, SubagentDefinition{}, "", errors.New("parent session and turn IDs are required")
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

func (m *subagentManager) startLocked(ctx context.Context, parentSessionID, parentTurnID, message, label string, definition SubagentDefinition, existing []storage.Subagent) (storage.Subagent, error) {
	// The durable store, rather than the in-memory handle map, is the source
	// of truth for the per-parent quota.
	open := 0
	for _, child := range existing {
		if child.Status != storage.SubagentStatusClosed {
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
		ID: id, DisplayName: displayName, Label: label, ParentSessionID: parentSessionID, ParentTurnID: parentTurnID,
		SessionID: sessionID, DefinitionName: definition.Name, Provider: definition.Provider, Model: definition.Model,
		Status: storage.SubagentStatusRunning, CurrentTurnID: turnID, CreatedAt: now, UpdatedAt: now,
	}
	record, err = m.store.Create(ctx, record)
	if err != nil {
		return storage.Subagent{}, err
	}

	child, err := m.createChild(definition)
	if err != nil {
		_, _ = m.store.Close(context.Background(), id)
		return storage.Subagent{}, fmt.Errorf("create child agent: %w", err)
	}
	instance := &managedSubagent{
		agent: child, runs: make(map[string]*agentruntime.Run),
		lastDispatchParentTurnID: parentTurnID,
		lastDispatchKey:          subagentMessageIdempotencyKey(parentSessionID, parentTurnID, id, message),
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = child.Close()
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
		_ = child.Close()
		_, _ = m.store.Close(context.Background(), id)
		return storage.Subagent{}, err
	}
	m.signalChanged()
	return m.getOwned(ctx, parentSessionID, id)
}

func (m *subagentManager) createChild(definition SubagentDefinition) (*Agent, error) {
	if m.childFactory != nil {
		return m.childFactory(definition)
	}
	model, err := m.project.ModelFor(definition.Provider, definition.Model)
	if err != nil {
		return nil, err
	}
	childProject := m.project.withSkills(definition.Skills)
	options := []Option{
		withChildAgent(),
		withChildProject(childProject),
		WithModel(model),
		withChildSystemPrompts(childProject, definition),
		WithMessageStorage(m.config.messages),
		WithPermissionStorage(m.config.permissions),
		WithConfirmationStorage(m.config.confirmations),
		WithPermissionPolicy(m.config.permissionPolicy),
		WithPermissionMode(m.parent.PermissionMode()),
		WithNonInteractive(m.config.nonInteractive),
		WithToolWorkers(m.config.toolWorkers),
		WithChannelBuffer(m.config.channelBuffer),
		WithSkillReloadPolicy(m.config.skillReload),
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

// withChildProject retains the project material a worker needs without
// deciding how that material is presented to the provider. A child has no
// manager and consequently no recursive management tools.
func withChildProject(project *Project) Option {
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

// List returns only records owned by the requested parent session.
func (m *subagentManager) List(ctx context.Context, parentSessionID string, includeClosed bool) ([]storage.Subagent, error) {
	if strings.TrimSpace(parentSessionID) == "" {
		return nil, errors.New("parent session ID is required")
	}
	records, err := m.store.ListByParent(nonNilContext(ctx), parentSessionID)
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
// omitted cursor resumes from the child's stored cursor instead of replaying
// its full transcript.
func (m *subagentManager) Read(ctx context.Context, parentSessionID, id, afterMessageID string) (subagentReadResult, error) {
	record, err := m.getOwned(nonNilContext(ctx), parentSessionID, id)
	if err != nil {
		return subagentReadResult{}, err
	}
	messages, err := m.parent.ListMessages(nonNilContext(ctx), record.SessionID)
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
			return subagentReadResult{}, fmt.Errorf("child message cursor %q was not found", cursor)
		}
	}
	delta := messages[start:]
	result := subagentReadResult{Subagent: record, NextMessageID: cursor}
	for index := len(delta) - 1; index >= 0; index-- {
		message := delta[index]
		if message.Type == agentruntime.MessageTypeAssistant && (record.LastTurnID == "" || message.TurnID == record.LastTurnID) {
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
		result.NextMessageID = last.ID
		m.signalChanged()
	}
	return result, nil
}

// Wait blocks until an owned child has unread transcript activity or its
// storage version advances beyond a caller's cursor. Cancelling ctx cancels
// only this wait; it never interrupts the child.
func (m *subagentManager) Wait(ctx context.Context, parentSessionID string, ids []string, after map[string]uint64) ([]storage.Subagent, error) {
	ctx = nonNilContext(ctx)
	for _, id := range ids {
		if _, err := m.getOwned(ctx, parentSessionID, id); err != nil {
			return nil, err
		}
	}
	if after == nil {
		// Before taking a state snapshot, unread output is immediately useful
		// to the caller. Once it is all observed, the snapshot makes later
		// close/idle/running transitions wake this wait too.
		unread, err := m.changedSince(ctx, parentSessionID, ids, nil)
		if err != nil || len(unread) != 0 {
			return unread, err
		}
		records, err := m.List(ctx, parentSessionID, true)
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
		changed, err := m.changedSince(ctx, parentSessionID, ids, after)
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
// children become targets.
func (m *subagentManager) WaitForTurnCompletion(ctx context.Context, parentSessionID string, ids []string) ([]storage.Subagent, error) {
	ctx = nonNilContext(ctx)
	records, err := m.List(ctx, parentSessionID, true)
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
		if record.Status != storage.SubagentStatusRunning || record.CurrentTurnID == "" {
			ready = append(ready, record)
			continue
		}
		targetTurns[id] = record.CurrentTurnID
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
			record, getErr := m.getOwned(ctx, parentSessionID, id)
			if getErr != nil {
				return nil, getErr
			}
			if record.Status == storage.SubagentStatusClosed || record.LastTurnID == turnID {
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

func (m *subagentManager) changedSince(ctx context.Context, parentSessionID string, ids []string, after map[string]uint64) ([]storage.Subagent, error) {
	records, err := m.List(ctx, parentSessionID, true)
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
		messages, listErr := m.parent.ListMessages(ctx, record.SessionID)
		if listErr != nil {
			return nil, listErr
		}
		if len(messages) != 0 && messages[len(messages)-1].ID != record.ObservedMessageID {
			changed = append(changed, record)
		}
	}
	return changed, nil
}

// Send delivers direct application/UI work without parent-turn deduplication.
// Model-facing calls use SendFromParentTurn so retries cannot multiply work.
func (m *subagentManager) Send(ctx context.Context, parentSessionID, id, content string) (storage.Subagent, error) {
	ctx = nonNilContext(ctx)
	content = normalizeSubagentMessage(content)
	if content == "" {
		return storage.Subagent{}, errors.New("subagent message is required")
	}
	record, err := m.getOwned(ctx, parentSessionID, id)
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
	// Refresh under the per-child turn gate so a monitor cannot race a
	// completion into an accidental concurrent turn.
	record, err = m.getOwned(ctx, parentSessionID, id)
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

// StatusFromParentTurn returns at most one fresh lifecycle snapshot for a
// child in a parent turn. Later calls in the same turn receive the original
// snapshot so model-facing status checks cannot become polling.
func (m *subagentManager) StatusFromParentTurn(ctx context.Context, parentSessionID, parentTurnID, id string) (toolexecution.SubagentStatusSnapshot, error) {
	ctx = nonNilContext(ctx)
	parentTurnID = strings.TrimSpace(parentTurnID)
	if parentTurnID == "" {
		return toolexecution.SubagentStatusSnapshot{}, errors.New("parent turn ID is required")
	}
	if _, err := m.getOwned(ctx, parentSessionID, id); err != nil {
		return toolexecution.SubagentStatusSnapshot{}, err
	}
	instance, err := m.instance(id)
	if err != nil {
		return toolexecution.SubagentStatusSnapshot{}, err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.lastStatusParentTurnID == parentTurnID {
		return toolexecution.SubagentStatusSnapshot{Subagent: storage.CloneSubagent(instance.lastStatusSnapshot), Repeated: true}, nil
	}
	record, err := m.getOwned(ctx, parentSessionID, id)
	if err != nil {
		return toolexecution.SubagentStatusSnapshot{}, err
	}
	instance.lastStatusParentTurnID = parentTurnID
	instance.lastStatusSnapshot = storage.CloneSubagent(record)
	return toolexecution.SubagentStatusSnapshot{Subagent: record}, nil
}

// SendFromParentTurn accepts at most one dispatch from a parent turn to one
// child. Exact retries return duplicate; changed retries return already_sent,
// and both decisions precede lifecycle admission. A pending authoritative
// callback is also a controlled non-error result so the model can end its turn
// without inventing a replacement response. None of these cases adds work.
func (m *subagentManager) SendFromParentTurn(ctx context.Context, parentSessionID, parentTurnID, id, content string) (toolexecution.SubagentSendResult, error) {
	ctx = nonNilContext(ctx)
	parentTurnID = strings.TrimSpace(parentTurnID)
	content = normalizeSubagentMessage(content)
	if parentTurnID == "" {
		return toolexecution.SubagentSendResult{}, errors.New("parent turn ID is required")
	}
	if content == "" {
		return toolexecution.SubagentSendResult{}, errors.New("subagent message is required")
	}
	record, err := m.getOwned(ctx, parentSessionID, id)
	if err != nil {
		return toolexecution.SubagentSendResult{}, err
	}
	instance, err := m.instance(id)
	if err != nil {
		return toolexecution.SubagentSendResult{}, err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	record, err = m.getOwned(ctx, parentSessionID, id)
	if err != nil {
		return toolexecution.SubagentSendResult{}, err
	}
	key := subagentMessageIdempotencyKey(parentSessionID, parentTurnID, id, content)
	if instance.lastDispatchParentTurnID == parentTurnID {
		action := toolexecution.SubagentSendAlreadySent
		deduplicated := false
		if instance.lastDispatchKey == key {
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
	if err := m.validateSubagentSend(ctx, record); err != nil {
		if errors.Is(err, storage.ErrSubagentCallbackPending) {
			return toolexecution.SubagentSendResult{
				Action: toolexecution.SubagentSendCallbackPending, Subagent: record,
				IdempotencyKey: key, Accepted: false,
			}, nil
		}
		return toolexecution.SubagentSendResult{}, err
	}
	action := toolexecution.SubagentSendStarted
	if record.Status == storage.SubagentStatusRunning {
		action = toolexecution.SubagentSendQueued
	}
	updated, err := m.sendLocked(ctx, instance, record, content)
	if err != nil {
		return toolexecution.SubagentSendResult{}, err
	}
	instance.lastDispatchParentTurnID = parentTurnID
	instance.lastDispatchKey = key
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
		_, _ = m.transition(context.Background(), record.ID, storage.SubagentStatusIdle, "", turnID, err.Error(), storage.SubagentTurnFailed, "", "")
		return storage.Subagent{}, err
	}
	m.signalChanged()
	return m.getOwned(ctx, record.ParentSessionID, record.ID)
}

// validateSubagentSend allows running work to accept ordered mailbox input.
// An idle child may resume after any known outcome, but only after the parent
// consumed that outcome's callback; otherwise a new message could overtake the
// authoritative result that explains what the child needs next.
func (m *subagentManager) validateSubagentSend(ctx context.Context, record storage.Subagent) error {
	if record.Status == storage.SubagentStatusRunning {
		return nil
	}
	if record.Status == storage.SubagentStatusClosed {
		return storage.ErrSubagentClosed
	}
	switch record.LastTurnOutcome {
	case storage.SubagentTurnCompleted, storage.SubagentTurnIncomplete, storage.SubagentTurnFailed:
		return m.validateLatestSubagentCallbackObserved(ctx, record)
	default:
		return storage.ErrSubagentOutcomeUnavailable
	}
}

func normalizeSubagentMessage(content string) string {
	return strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
}

func subagentMessageIdempotencyKey(parentSessionID, parentTurnID, subagentID, content string) string {
	payload := strings.Join([]string{
		"subagent-message-v1", parentSessionID, parentTurnID, subagentID,
		normalizeSubagentMessage(content),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// Interrupt stops only the current child turn. The child instance remains
// idle and can accept another Send after the terminal event is recorded.
func (m *subagentManager) Interrupt(ctx context.Context, parentSessionID, id, reason string) error {
	if _, err := m.getOwned(nonNilContext(ctx), parentSessionID, id); err != nil {
		return err
	}
	instance, err := m.instance(id)
	if err != nil {
		return err
	}
	instance.mu.Lock()
	run := instance.run
	child := instance.agent
	instance.mu.Unlock()
	if child == nil || run == nil || run.Done() {
		return nil
	}
	return run.Interrupt(nonNilContext(ctx), reason)
}

// watchParentRun propagates an interrupted parent turn only to children that
// were created by that exact tool chain. It intentionally does not react to a
// normal parent completion: delegated work may outlive the tool invocation.
func (m *subagentManager) watchParentRun(run *agentruntime.Run) {
	if run == nil {
		return
	}
	go func() {
		if parentRunInterrupted(run.Events()) {
			m.interruptParentTurn(run.SessionID(), run.TurnID())
			return
		}
		subscription := run.Subscribe(m.ctx)
		for event := range subscription.Events {
			if event.Type == agentruntime.AgentInterrupted {
				m.interruptParentTurn(run.SessionID(), run.TurnID())
				return
			}
		}
		// A fast interruption can happen between the retained snapshot and
		// subscription registration; the final retained check closes that gap.
		if parentRunInterrupted(run.Events()) {
			m.interruptParentTurn(run.SessionID(), run.TurnID())
		}
	}()
}

func parentRunInterrupted(events []agentruntime.AgentEvent) bool {
	for _, event := range events {
		if event.Type == agentruntime.AgentInterrupted {
			return true
		}
	}
	return false
}

func (m *subagentManager) interruptParentTurn(parentSessionID, parentTurnID string) {
	records, err := m.store.ListByParent(context.Background(), parentSessionID)
	if err != nil {
		return
	}
	for _, record := range records {
		if record.ParentTurnID != parentTurnID || record.Status != storage.SubagentStatusRunning {
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
			_ = run.Interrupt(context.Background(), "parent turn interrupted")
		}
	}
}

// Close prevents new work, drops queued work, and retains transcript history.
// A running child must first finish or be interrupted; closing is lifecycle
// cleanup and never doubles as cancellation.
func (m *subagentManager) CloseSubagent(ctx context.Context, parentSessionID, id string) (storage.Subagent, error) {
	ctx = nonNilContext(ctx)
	record, err := m.getOwned(ctx, parentSessionID, id)
	if err != nil {
		return storage.Subagent{}, err
	}
	instance, instanceErr := m.instance(id)
	if instanceErr != nil {
		if record.Status == storage.SubagentStatusRunning {
			return storage.Subagent{}, storage.ErrSubagentRunning
		}
		if err := m.validateSubagentClose(ctx, record); err != nil {
			return storage.Subagent{}, err
		}
		closed, closeErr := m.store.Close(ctx, id)
		if closeErr == nil {
			m.signalChanged()
		}
		return closed, closeErr
	}
	instance.mu.Lock()
	record, err = m.getOwned(ctx, parentSessionID, id)
	if err != nil {
		instance.mu.Unlock()
		return storage.Subagent{}, err
	}
	if record.Status == storage.SubagentStatusRunning {
		instance.mu.Unlock()
		return storage.Subagent{}, storage.ErrSubagentRunning
	}
	if err := m.validateSubagentClose(ctx, record); err != nil {
		instance.mu.Unlock()
		return storage.Subagent{}, err
	}
	closed, err := m.store.Close(ctx, id)
	if err != nil {
		instance.mu.Unlock()
		return storage.Subagent{}, err
	}
	child := instance.agent
	instance.agent = nil
	instance.mu.Unlock()
	if child != nil {
		_ = child.Close()
	}
	m.signalChanged()
	return closed, nil
}

// ForceCloseSubagent bypasses normal outcome and callback-consumption guards
// for an explicit user-directed destructive close. It atomically marks the
// child closed before interrupting its run so completion cannot dequeue more
// mailbox work or publish a fresh callback after the force close begins.
func (m *subagentManager) ForceCloseSubagent(ctx context.Context, parentSessionID, id string) (toolexecution.SubagentForceCloseResult, error) {
	ctx = nonNilContext(ctx)
	record, err := m.getOwned(ctx, parentSessionID, id)
	if err != nil {
		return toolexecution.SubagentForceCloseResult{}, err
	}
	if record.Status == storage.SubagentStatusClosed {
		return toolexecution.SubagentForceCloseResult{}, storage.ErrSubagentClosed
	}
	instance, instanceErr := m.instance(id)
	if instanceErr != nil {
		if record.Status == storage.SubagentStatusRunning {
			return toolexecution.SubagentForceCloseResult{}, fmt.Errorf("force close running subagent: runtime instance unavailable: %w", instanceErr)
		}
		closed, closeErr := m.store.Close(ctx, id)
		if closeErr != nil {
			return toolexecution.SubagentForceCloseResult{}, closeErr
		}
		m.signalChanged()
		return toolexecution.SubagentForceCloseResult{
			Subagent: closed, PreviousStatus: record.Status, PreviousOutcome: record.LastTurnOutcome,
			DroppedMessages: len(record.Pending),
		}, nil
	}

	instance.mu.Lock()
	record, err = m.getOwned(ctx, parentSessionID, id)
	if err != nil {
		instance.mu.Unlock()
		return toolexecution.SubagentForceCloseResult{}, err
	}
	if record.Status == storage.SubagentStatusClosed {
		instance.mu.Unlock()
		return toolexecution.SubagentForceCloseResult{}, storage.ErrSubagentClosed
	}
	run := instance.run
	child := instance.agent
	closed, err := m.store.Close(ctx, id)
	if err != nil {
		instance.mu.Unlock()
		return toolexecution.SubagentForceCloseResult{}, err
	}
	instance.agent = nil
	instance.mu.Unlock()

	interrupted := record.Status == storage.SubagentStatusRunning && run != nil && !run.Done()
	if interrupted {
		_ = run.Interrupt(context.Background(), "subagent force closed by explicit user request")
	}
	if child != nil {
		_ = child.Close()
	}
	m.signalChanged()
	return toolexecution.SubagentForceCloseResult{
		Subagent: closed, PreviousStatus: record.Status, PreviousOutcome: record.LastTurnOutcome,
		DroppedMessages: len(record.Pending), Interrupted: interrupted,
	}, nil
}

// validateSubagentClose makes close a cleanup-only operation. An idle state
// alone is insufficient: incomplete work must remain available for follow-up,
// while completed and failed outcomes must reach a parent callback consumer
// before the child can disappear from the active set.
func (m *subagentManager) validateSubagentClose(ctx context.Context, record storage.Subagent) error {
	switch record.LastTurnOutcome {
	case storage.SubagentTurnCompleted, storage.SubagentTurnFailed:
		return m.validateLatestSubagentCallbackObserved(ctx, record)
	case storage.SubagentTurnIncomplete:
		return storage.ErrSubagentIncomplete
	default:
		return storage.ErrSubagentOutcomeUnavailable
	}
}

func (m *subagentManager) validateLatestSubagentCallbackObserved(ctx context.Context, record storage.Subagent) error {
	messages, err := m.parent.ListMessages(nonNilContext(ctx), record.SessionID)
	if err != nil {
		return fmt.Errorf("read child callback cursor: %w", err)
	}
	if len(messages) == 0 {
		return storage.ErrSubagentCallbackPending
	}
	latest := messages[len(messages)-1]
	if record.ObservedMessageID != latest.ID || record.ObservedVersion < uint64(len(messages)) {
		return storage.ErrSubagentCallbackPending
	}
	return nil
}

// Run returns the retained child run for nested SSE backfill and live
// subscription. It deliberately remains parent-ownership checked.
func (m *subagentManager) Run(ctx context.Context, parentSessionID, id, turnID string) (*agentruntime.Run, error) {
	if _, err := m.getOwned(nonNilContext(ctx), parentSessionID, id); err != nil {
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
		SessionID: record.SessionID, TurnID: turnID,
		Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: content},
	})
	if err != nil {
		return fmt.Errorf("start child turn: %w", err)
	}
	// Runtime.StartSubscribed fences RunStarted, not the subsequent append of
	// its initial user message. A manager Start has a stronger, useful
	// contract: once it returns, Read can immediately render the delegated
	// input. Wait only for that local storage commit; provider completion is
	// deliberately not part of this boundary.
	if err := m.waitForInitialInput(record.SessionID, turnID, run); err != nil {
		_ = run.Interrupt(context.Background(), "initial child input was not committed")
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
			return fmt.Errorf("observe initial child input: %w", err)
		}
		for _, message := range messages {
			if message.TurnID == turnID && message.Type == agentruntime.MessageTypeUser {
				return nil
			}
		}
		if run.Done() {
			if _, err := run.Result(); err != nil {
				return fmt.Errorf("initial child input was not committed: %w", err)
			}
			return errors.New("initial child input was not committed")
		}
		select {
		case <-m.ctx.Done():
			return ErrClosed
		case <-deadline.C:
			return errors.New("timed out waiting for initial child input")
		case <-ticker.C:
		}
	}
}

func (m *subagentManager) monitor(id string, instance *managedSubagent, run *agentruntime.Run, subscription agentruntime.EventSubscription) {
	for range subscription.Events {
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
	messages, messagesErr := m.config.messages.List(context.Background(), record.SessionID)
	if messagesErr != nil && lastTurnError == "" {
		lastTurnError = "read completed child output: " + messagesErr.Error()
	}
	lastOutcome := storage.SubagentTurnIncomplete
	lastSummary := "Child turn ended without an explicit outcome report."
	lastNextStep := "Review the final answer and send one focused follow-up if required."
	if lastTurnError != "" {
		lastOutcome = storage.SubagentTurnFailed
		lastSummary = ""
		lastNextStep = ""
	} else if reported, ok := reportedSubagentOutcome(run.TurnID(), messages); ok {
		lastSummary = reported.Summary
		lastNextStep = reported.NextStep
		if reported.Status == toolexecution.SubagentOutcomeCompleted {
			lastOutcome = storage.SubagentTurnCompleted
			lastNextStep = ""
		}
	}
	completed, err := m.transition(context.Background(), id, storage.SubagentStatusIdle, "", run.TurnID(), lastTurnError, lastOutcome, lastSummary, lastNextStep)
	if err != nil {
		return
	}
	instance.run = nil
	m.signalChanged()
	callback := callbackFromMessages(completed, messages)
	m.publishCallback(callback)
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
		_, _ = m.transition(context.Background(), id, storage.SubagentStatusIdle, "", turnID, err.Error(), storage.SubagentTurnFailed, "", "")
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
		child := instance.agent
		instance.mu.Unlock()
		if child != nil {
			if err := child.SetPermissionMode(nonNilContext(ctx), mode); err != nil && !errors.Is(err, ErrClosed) && first == nil {
				first = err
			}
		}
	}
	return first
}

// transition retries optimistic updates that race a Read observation. The
// lifecycle decision is still serialized by the child instance lock; this
// retry solely accommodates the intentionally independent read cursor.
func (m *subagentManager) transition(ctx context.Context, id string, status storage.SubagentStatus, currentTurnID, lastTurnID, lastTurnError string, lastTurnOutcome storage.SubagentTurnOutcome, lastTurnSummary, lastTurnNextStep string) (storage.Subagent, error) {
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
		outcome := lastTurnOutcome
		summary := lastTurnSummary
		nextStep := lastTurnNextStep
		if last == "" {
			last = record.LastTurnID
			turnError = record.LastTurnError
			outcome = record.LastTurnOutcome
			summary = record.LastTurnSummary
			nextStep = record.LastTurnNextStep
		}
		updated, err := m.store.Update(ctx, id, record.Version, storage.SubagentUpdate{
			Status: status, CurrentTurnID: currentTurnID, LastTurnID: last, LastTurnError: turnError,
			LastTurnOutcome: outcome, LastTurnSummary: summary, LastTurnNextStep: nextStep,
		})
		if !errors.Is(err, storage.ErrSubagentVersionConflict) {
			return updated, err
		}
	}
	return storage.Subagent{}, storage.ErrSubagentVersionConflict
}

// Close closes every live child before the root executor is cancelled. This
// order prevents a parent tool wait from being stranded on a child executor.
func (m *subagentManager) Close() error {
	m.closeCallbacks()
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
		if child := instance.releaseAgent(); child != nil {
			if err := child.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	m.signalChanged()
	return first
}

func (m *subagentManager) getOwned(ctx context.Context, parentSessionID, id string) (storage.Subagent, error) {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(id) == "" {
		return storage.Subagent{}, storage.ErrSubagentNotFound
	}
	record, found, err := m.store.Get(ctx, id)
	if err != nil {
		return storage.Subagent{}, err
	}
	if !found || record.ParentSessionID != parentSessionID {
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
	child := instance.agent
	instance.agent = nil
	return child
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
