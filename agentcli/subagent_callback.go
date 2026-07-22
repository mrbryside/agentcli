package agentcli

import (
	"context"
	"encoding/json"
	"fmt"

	"harness-api/agentruntime"
	"harness-api/storage"
)

// SubagentCallbackStatus describes how one child turn ended.
type SubagentCallbackStatus string

const (
	SubagentCallbackCompleted SubagentCallbackStatus = "completed"
	SubagentCallbackFailed    SubagentCallbackStatus = "failed"
)

// SubagentCallback is the compact, live-only completion signal emitted for a
// child turn. The child transcript remains available through ListMessages;
// this value carries only the final assistant answer and terminal error.
type SubagentCallback struct {
	ParentSessionID string
	ParentTurnID    string
	SubagentID      string
	SubagentName    string
	DisplayName     string
	SessionID       string
	TurnID          string
	Status          SubagentCallbackStatus
	FinalAnswer     *agentruntime.Message
	Error           string
	NextMessageID   string
	MessageCount    uint64
}

// RuntimeMessage converts the callback into trusted provider-neutral input
// for a new parent turn. It is deliberately not represented as a human user
// message or as a late result for an already-resolved tool call.
func (callback SubagentCallback) RuntimeMessage() agentruntime.Message {
	finalAnswer := ""
	if callback.FinalAnswer != nil && callback.FinalAnswer.Content != "" {
		finalAnswer = callback.FinalAnswer.Content
	}
	payload, _ := json.Marshal(struct {
		ID             string                 `json:"id"`
		DisplayName    string                 `json:"display_name"`
		DefinitionName string                 `json:"definition_name"`
		TurnID         string                 `json:"turn_id"`
		Status         SubagentCallbackStatus `json:"status"`
		Error          string                 `json:"error,omitempty"`
		FinalAnswer    string                 `json:"final_answer,omitempty"`
		Instruction    string                 `json:"instruction"`
	}{
		ID: callback.SubagentID, DisplayName: callback.DisplayName, DefinitionName: callback.SubagentName, TurnID: callback.TurnID,
		Status: callback.Status, Error: callback.Error, FinalAnswer: finalAnswer,
		Instruction: "This callback is the authoritative outcome for this child turn. Refer to the child by display_name when speaking with the user. Use the final answer or error now, do useful work while other children continue, send a focused follow-up if essential information is missing, or end this parent turn and wait passively. Never call list_subagents or subagent_status to wait or check for another callback; unfinished children will callback automatically. After a follow-up, do not poll for it. Treat a bounded one-shot task as finished after you consume and deliver its result: close the child unless there is a concrete planned follow-up, queued work, unresolved work requiring the same context, or an explicit ongoing collaboration. The mere possibility that the user may ask something later is not a reason to keep it open. Never reveal secret values from a child result; redact them and warn the user.",
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
	status := SubagentCallbackCompleted
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
		Error:           record.LastTurnError,
		MessageCount:    uint64(len(messages)),
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
