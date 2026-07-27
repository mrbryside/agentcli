package agentcli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/toolexecution"
)

// SubagentCallbackStatus describes how one child turn ended.
type SubagentCallbackStatus string

const (
	SubagentCallbackCompleted  SubagentCallbackStatus = "completed"
	SubagentCallbackIncomplete SubagentCallbackStatus = "incomplete"
	SubagentCallbackFailed     SubagentCallbackStatus = "failed"
)

// SubagentCallback is the compact, live-only outcome signal emitted for a
// child turn. The child transcript remains available through ListMessages;
// this value carries the semantic outcome, final assistant answer, and error.
type SubagentCallback struct {
	ParentSessionID string
	ParentTurnID    string
	SubagentID      string
	SubagentName    string
	DisplayName     string
	SessionID       string
	TurnID          string
	Status          SubagentCallbackStatus
	Summary         string
	NextStep        string
	FinalAnswer     *agentruntime.Message
	Error           string
	NextMessageID   string
	MessageCount    uint64
}

// RuntimeMessage converts the callback into trusted provider-neutral input
// for a new parent turn. It is deliberately not represented as a human user
// message or as a late result for an already-resolved tool call. A supplied
// callback-progress snapshot describes the complete originating response
// scope at the instant this callback was reserved.
func (callback SubagentCallback) RuntimeMessage(progress ...toolexecution.ResponseScopeCallbackProgress) agentruntime.Message {
	finalAnswer := ""
	if callback.FinalAnswer != nil && callback.FinalAnswer.Content != "" {
		finalAnswer = callback.FinalAnswer.Content
	}
	var callbackProgress *toolexecution.ResponseScopeCallbackProgress
	if len(progress) != 0 {
		snapshot := progress[0]
		callbackProgress = &snapshot
	}
	payload, _ := json.Marshal(struct {
		ID               string                                       `json:"id"`
		DisplayName      string                                       `json:"display_name"`
		DefinitionName   string                                       `json:"definition_name"`
		TurnID           string                                       `json:"turn_id"`
		Status           SubagentCallbackStatus                       `json:"status"`
		Error            string                                       `json:"error,omitempty"`
		Summary          string                                       `json:"summary,omitempty"`
		NextStep         string                                       `json:"next_step,omitempty"`
		FinalAnswer      string                                       `json:"final_answer,omitempty"`
		CallbackProgress *toolexecution.ResponseScopeCallbackProgress `json:"callback_progress,omitempty"`
		Instruction      string                                       `json:"instruction"`
	}{
		ID: callback.SubagentID, DisplayName: callback.DisplayName, DefinitionName: callback.SubagentName, TurnID: callback.TurnID,
		Status: callback.Status, Error: callback.Error, Summary: callback.Summary, NextStep: callback.NextStep, FinalAnswer: finalAnswer,
		CallbackProgress: callbackProgress,
		Instruction:      "This is the authoritative child result. Read callback_progress before deciding what to do. pending_callbacks identifies assigned work whose callback is still outstanding; never duplicate, replace, retry, or poll it. received_callbacks identifies callback turns already delivered in this response scope; process each exactly once. If pending callbacks remain, continue only work that is independent of every pending callback and will not duplicate or invalidate it; otherwise wait without calling a response or delivery tool. When none remain, combine the received outcomes before finishing. For completed, use final_answer or summary. For incomplete, use next_step for one focused follow-up or user question. For failed, report the error and recover only when concrete work remains.",
	})
	content := "<subagent_callback>\n" + string(payload) + "\n</subagent_callback>"
	return agentruntime.Message{Type: agentruntime.MessageTypeRuntimeEvent, Content: content}
}

type subagentCallbackSubscriber struct {
	channel chan SubagentCallback
	notify  chan struct{}
	queue   []SubagentCallback
	closed  bool
}

func (m *subagentManager) subscribeCallbacks(ctx context.Context) <-chan SubagentCallback {
	ctx = nonNilContext(ctx)
	subscriber := &subagentCallbackSubscriber{channel: make(chan SubagentCallback, 8), notify: make(chan struct{}, 1)}
	var id uint64
	m.callbackMu.Lock()
	if m.callbacksClosed {
		subscriber.closed = true
	} else {
		m.nextCallbackSubscriber++
		id = m.nextCallbackSubscriber
		m.callbackSubscribers[id] = subscriber
	}
	m.callbackMu.Unlock()
	go m.deliverCallbacks(ctx, id, subscriber)
	return subscriber.channel
}

func (m *subagentManager) deliverCallbacks(ctx context.Context, id uint64, subscriber *subagentCallbackSubscriber) {
	defer close(subscriber.channel)
	defer func() {
		if id == 0 {
			return
		}
		m.callbackMu.Lock()
		delete(m.callbackSubscribers, id)
		m.callbackMu.Unlock()
	}()
	for {
		m.callbackMu.Lock()
		if len(subscriber.queue) != 0 {
			callback := cloneSubagentCallback(subscriber.queue[0])
			subscriber.queue = subscriber.queue[1:]
			m.callbackMu.Unlock()
			select {
			case subscriber.channel <- callback:
			case <-ctx.Done():
				return
			}
			continue
		}
		closed := subscriber.closed
		m.callbackMu.Unlock()
		if closed {
			return
		}
		select {
		case <-subscriber.notify:
		case <-ctx.Done():
			return
		}
	}
}

func (m *subagentManager) publishCallback(callback SubagentCallback) {
	m.callbackMu.Lock()
	defer m.callbackMu.Unlock()
	if m.callbacksClosed {
		return
	}
	for _, subscriber := range m.callbackSubscribers {
		subscriber.queue = append(subscriber.queue, cloneSubagentCallback(callback))
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func (m *subagentManager) closeCallbacks() {
	m.callbackMu.Lock()
	defer m.callbackMu.Unlock()
	if m.callbacksClosed {
		return
	}
	m.callbacksClosed = true
	for _, subscriber := range m.callbackSubscribers {
		subscriber.closed = true
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func cloneSubagentCallback(callback SubagentCallback) SubagentCallback {
	clone := callback
	if callback.FinalAnswer != nil {
		answer := storage.CloneMessage(*callback.FinalAnswer)
		clone.FinalAnswer = &answer
	}
	return clone
}

func callbackFromMessages(record storage.Subagent, messages []agentruntime.Message) SubagentCallback {
	status := SubagentCallbackIncomplete
	switch record.LastTurnOutcome {
	case storage.SubagentTurnCompleted:
		status = SubagentCallbackCompleted
	case storage.SubagentTurnFailed:
		status = SubagentCallbackFailed
	case storage.SubagentTurnIncomplete:
		status = SubagentCallbackIncomplete
	default:
		if record.LastTurnError != "" {
			status = SubagentCallbackFailed
		}
	}
	if record.LastTurnError != "" {
		status = SubagentCallbackFailed
	}
	callback := SubagentCallback{
		ParentSessionID: record.ParentSessionID,
		ParentTurnID:    record.ParentTurnID,
		SubagentID:      record.ID,
		SubagentName:    record.DefinitionName,
		DisplayName:     record.DisplayName,
		SessionID:       record.SessionID,
		TurnID:          record.LastTurnID,
		Status:          status,
		Summary:         record.LastTurnSummary,
		NextStep:        record.LastTurnNextStep,
		Error:           record.LastTurnError,
		MessageCount:    uint64(len(messages)),
	}
	if status != SubagentCallbackFailed && record.LastTurnOutcome == "" {
		if outcome, found := reportedSubagentOutcome(record.LastTurnID, messages); found {
			callback.Summary = outcome.Summary
			callback.NextStep = outcome.NextStep
			switch outcome.Status {
			case toolexecution.SubagentOutcomeCompleted:
				callback.Status = SubagentCallbackCompleted
			case toolexecution.SubagentOutcomeFailed:
				callback.Status = SubagentCallbackFailed
				callback.Error = outcome.Error
				callback.NextStep = ""
			}
		}
	}
	if len(messages) != 0 {
		callback.NextMessageID = messages[len(messages)-1].ID
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.TurnID == record.LastTurnID && message.Type == agentruntime.MessageTypeAssistant {
			answer := storage.CloneMessage(message)
			callback.FinalAnswer = &answer
			break
		}
	}
	return callback
}

func reportedSubagentOutcome(turnID string, messages []agentruntime.Message) (toolexecution.SubagentOutcome, bool) {
	var reported toolexecution.SubagentOutcome
	found := false
	for _, message := range messages {
		if message.TurnID != turnID || message.Type != agentruntime.MessageTypeToolResult || message.ToolResult == nil {
			continue
		}
		result := message.ToolResult
		if result.Name != toolexecution.SubagentOutcomeToolName || result.Status != agentruntime.ToolResultSucceeded {
			continue
		}
		outcome, err := toolexecution.ParseSubagentOutcome(result.Output)
		if err != nil {
			continue
		}
		reported = outcome
		found = true
	}
	return reported, found
}

// observeCallback advances the fallback read/reminder cursor only after a
// parent continuation has actually started.
func (m *subagentManager) observeCallback(ctx context.Context, callback SubagentCallback) error {
	record, err := m.getOwned(nonNilContext(ctx), callback.ParentSessionID, callback.SubagentID)
	if err != nil {
		return err
	}
	if record.SessionID != callback.SessionID {
		return fmt.Errorf("subagent callback session does not match the child")
	}
	if callback.NextMessageID == "" || callback.MessageCount == 0 {
		return nil
	}
	messages, err := m.parent.ListMessages(nonNilContext(ctx), record.SessionID)
	if err != nil {
		return err
	}
	if callback.MessageCount > uint64(len(messages)) {
		return fmt.Errorf("subagent callback cursor is beyond the child transcript")
	}
	message := messages[callback.MessageCount-1]
	if message.ID != callback.NextMessageID || message.TurnID != callback.TurnID {
		return fmt.Errorf("subagent callback cursor does not match the child turn")
	}
	_, err = m.store.Observe(nonNilContext(ctx), callback.SubagentID, callback.NextMessageID, callback.MessageCount)
	if err == nil {
		m.signalChanged()
	}
	return err
}
