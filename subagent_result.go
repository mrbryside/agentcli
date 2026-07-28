package agentcli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/toolexecution"
)

// SubagentResultStatus describes how one subagent turn ended.
type SubagentResultStatus string

const (
	SubagentResultCompleted  SubagentResultStatus = "completed"
	SubagentResultIncomplete SubagentResultStatus = "incomplete"
	SubagentResultFailed     SubagentResultStatus = "failed"
)

// SubagentResult is the compact, live-only signal emitted for a subagent turn.
// The subagent transcript remains available through ListMessages; this value
// carries the final assistant answer and any runtime error.
type SubagentResult struct {
	MainAgentSessionID string
	MainAgentTurnID    string
	SubagentID         string
	DefinitionName     string
	DisplayName        string
	SubagentSessionID  string
	SubagentTurnID     string
	Status             SubagentResultStatus
	Summary            string
	NextStep           string
	FinalAnswer        *agentruntime.Message
	Error              string
	LastMessageID      string
	MessageCount       uint64
}

// taskResultFromFinalOutput converts a completed child turn into the compact
// task protocol result. A result contract is validated once here; an invalid
// final response is a task error rather than a repair loop or a second child
// turn.
func taskResultFromFinalOutput(taskID string, definition SubagentDefinition, output string, incomplete bool) TaskResult {
	result := TaskResult{TaskID: taskID, AgentName: definition.Name}
	final, err := parseTaskFinalResult(definition, output)
	if err != nil {
		result.State = TaskStateError
		result.Error = err.Error()
		return result
	}
	result.Output = final.Output
	if incomplete {
		result.State = TaskStateIncomplete
	} else {
		result.State = TaskStateCompleted
	}
	return result
}

// RuntimeMessage converts the result into trusted provider-neutral input
// for a new main agent turn. It is deliberately not represented as a human user
// message or as a late result for an already-resolved tool call. A supplied
// result-progress snapshot describes the complete originating response
// scope at the instant this result was reserved.
func (result SubagentResult) RuntimeMessage(progress ...toolexecution.ResponseScopeResultProgress) agentruntime.Message {
	finalAnswer := ""
	if result.FinalAnswer != nil && result.FinalAnswer.Content != "" {
		finalAnswer = result.FinalAnswer.Content
	}
	var resultProgress *toolexecution.ResponseScopeResultProgress
	if len(progress) != 0 {
		snapshot := progress[0]
		resultProgress = &snapshot
	}
	payload, _ := json.Marshal(struct {
		SubagentID     string                                     `json:"subagent_id"`
		DisplayName    string                                     `json:"display_name"`
		DefinitionName string                                     `json:"definition_name"`
		SubagentTurnID string                                     `json:"subagent_turn_id"`
		Status         SubagentResultStatus                       `json:"status"`
		Error          string                                     `json:"error,omitempty"`
		Summary        string                                     `json:"summary,omitempty"`
		NextStep       string                                     `json:"next_step,omitempty"`
		FinalAnswer    string                                     `json:"final_answer,omitempty"`
		ResultProgress *toolexecution.ResponseScopeResultProgress `json:"result_progress,omitempty"`
		Instruction    string                                     `json:"instruction"`
	}{
		SubagentID: result.SubagentID, DisplayName: result.DisplayName, DefinitionName: result.DefinitionName, SubagentTurnID: result.SubagentTurnID,
		Status: result.Status, Error: result.Error, Summary: result.Summary, NextStep: result.NextStep, FinalAnswer: finalAnswer,
		ResultProgress: resultProgress,
		Instruction:    "This is an authoritative subagent result. Read result_progress first. pending_results lists assigned work whose result has not arrived; never duplicate, replace, retry, or poll it. delivered_results lists results already delivered for this user response; process each once. If pending_count is greater than zero, continue only specific independent main-agent work already planned. If none remains, stop without assistant content or another tool call. More results arrive automatically. When pending_count is zero, combine all delivered results before finishing. For completed, use final_answer or summary. For incomplete, use next_step for one focused follow-up or user question. For failed, use error and recover only when concrete work remains.",
	})
	content := "<subagent_result>\n" + string(payload) + "\n</subagent_result>"
	return agentruntime.Message{Type: agentruntime.MessageTypeRuntimeEvent, Content: content}
}

type subagentResultSubscriber struct {
	channel chan SubagentResult
	notify  chan struct{}
	queue   []SubagentResult
	closed  bool
}

func (m *subagentManager) subscribeResults(ctx context.Context) <-chan SubagentResult {
	ctx = nonNilContext(ctx)
	subscriber := &subagentResultSubscriber{channel: make(chan SubagentResult, 8), notify: make(chan struct{}, 1)}
	var id uint64
	m.resultMu.Lock()
	if m.resultsClosed {
		subscriber.closed = true
	} else {
		m.nextResultSubscriber++
		id = m.nextResultSubscriber
		m.resultSubscribers[id] = subscriber
	}
	m.resultMu.Unlock()
	go m.deliverResults(ctx, id, subscriber)
	return subscriber.channel
}

func (m *subagentManager) deliverResults(ctx context.Context, id uint64, subscriber *subagentResultSubscriber) {
	defer close(subscriber.channel)
	defer func() {
		if id == 0 {
			return
		}
		m.resultMu.Lock()
		delete(m.resultSubscribers, id)
		m.resultMu.Unlock()
	}()
	for {
		m.resultMu.Lock()
		if len(subscriber.queue) != 0 {
			result := cloneSubagentResult(subscriber.queue[0])
			subscriber.queue = subscriber.queue[1:]
			m.resultMu.Unlock()
			select {
			case subscriber.channel <- result:
			case <-ctx.Done():
				return
			}
			continue
		}
		closed := subscriber.closed
		m.resultMu.Unlock()
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

func (m *subagentManager) publishResult(result SubagentResult) {
	m.resultMu.Lock()
	defer m.resultMu.Unlock()
	if m.resultsClosed {
		return
	}
	for _, subscriber := range m.resultSubscribers {
		subscriber.queue = append(subscriber.queue, cloneSubagentResult(result))
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func (m *subagentManager) closeResults() {
	m.resultMu.Lock()
	defer m.resultMu.Unlock()
	if m.resultsClosed {
		return
	}
	m.resultsClosed = true
	for _, subscriber := range m.resultSubscribers {
		subscriber.closed = true
		select {
		case subscriber.notify <- struct{}{}:
		default:
		}
	}
}

func cloneSubagentResult(result SubagentResult) SubagentResult {
	clone := result
	if result.FinalAnswer != nil {
		answer := storage.CloneMessage(*result.FinalAnswer)
		clone.FinalAnswer = &answer
	}
	return clone
}

func subagentResultFromMessages(record storage.Subagent, messages []agentruntime.Message) SubagentResult {
	status := SubagentResultIncomplete
	switch record.LastResultStatus {
	case storage.SubagentResultCompleted:
		status = SubagentResultCompleted
	case storage.SubagentResultFailed:
		status = SubagentResultFailed
	case storage.SubagentResultIncomplete:
		status = SubagentResultIncomplete
	default:
		if record.LastResultError != "" {
			status = SubagentResultFailed
		}
	}
	if record.LastResultError != "" {
		status = SubagentResultFailed
	}
	result := SubagentResult{
		MainAgentSessionID: record.MainAgentSessionID,
		MainAgentTurnID:    record.MainAgentTurnID,
		SubagentID:         record.ID,
		DefinitionName:     record.DefinitionName,
		DisplayName:        record.DisplayName,
		SubagentSessionID:  record.SubagentSessionID,
		SubagentTurnID:     record.LastSubagentTurnID,
		Status:             status,
		Summary:            record.LastResultSummary,
		NextStep:           record.LastResultNextStep,
		Error:              record.LastResultError,
		MessageCount:       uint64(len(messages)),
	}
	if len(messages) != 0 {
		result.LastMessageID = messages[len(messages)-1].ID
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.TurnID == record.LastSubagentTurnID && message.Type == agentruntime.MessageTypeAssistant {
			answer := storage.CloneMessage(message)
			result.FinalAnswer = &answer
			break
		}
	}
	return result
}

// observeSubagentResult advances the fallback read/reminder cursor only after a
// main agent continuation has actually started.
func (m *subagentManager) observeSubagentResult(ctx context.Context, result SubagentResult) error {
	record, err := m.getOwned(nonNilContext(ctx), result.MainAgentSessionID, result.SubagentID)
	if err != nil {
		return err
	}
	if record.SubagentSessionID != result.SubagentSessionID {
		return fmt.Errorf("subagent result session does not match the subagent")
	}
	if result.LastMessageID == "" || result.MessageCount == 0 {
		return nil
	}
	messages, err := m.mainAgent.ListMessages(nonNilContext(ctx), record.SubagentSessionID)
	if err != nil {
		return err
	}
	if result.MessageCount > uint64(len(messages)) {
		return fmt.Errorf("subagent result cursor is beyond the subagent transcript")
	}
	message := messages[result.MessageCount-1]
	if message.ID != result.LastMessageID || message.TurnID != result.SubagentTurnID {
		return fmt.Errorf("subagent result cursor does not match the subagent turn")
	}
	_, err = m.store.Observe(nonNilContext(ctx), result.SubagentID, result.LastMessageID, result.MessageCount)
	if err == nil {
		m.signalChanged()
	}
	return err
}
