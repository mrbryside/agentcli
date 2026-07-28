package storage

import (
	"errors"
	"testing"
	"time"
)

func TestValidateSubagent(t *testing.T) {
	valid := testSubagent()
	tests := []struct {
		name string
		edit func(*Subagent)
	}{
		{"missing ID", func(s *Subagent) { s.ID = "" }},
		{"missing display name", func(s *Subagent) { s.DisplayName = "" }},
		{"missing mainAgent session", func(s *Subagent) { s.MainAgentSessionID = "" }},
		{"missing mainAgent turn", func(s *Subagent) { s.MainAgentTurnID = "" }},
		{"missing subagent session", func(s *Subagent) { s.SubagentSessionID = "" }},
		{"missing definition", func(s *Subagent) { s.DefinitionName = "" }},
		{"missing provider", func(s *Subagent) { s.Provider = "" }},
		{"missing model", func(s *Subagent) { s.Model = "" }},
		{"invalid status", func(s *Subagent) { s.Status = "unknown" }},
		{"running without current turn", func(s *Subagent) { s.Status, s.CurrentSubagentTurnID = SubagentStatusRunning, "" }},
		{"closed without closed timestamp", func(s *Subagent) { s.Status, s.ClosedAt = SubagentStatusClosed, nil }},
		{"closed with current turn", func(s *Subagent) { s.Status, s.CurrentSubagentTurnID = SubagentStatusClosed, "turn_1" }},
		{"queued message missing ID", func(s *Subagent) { s.Pending[0].ID = "" }},
		{"queued message missing content", func(s *Subagent) { s.Pending[0].Content = "" }},
		{"queued message missing timestamp", func(s *Subagent) { s.Pending[0].CreatedAt = time.Time{} }},
		{"outcome without turn", func(s *Subagent) {
			s.LastSubagentTurnID, s.LastResultStatus, s.LastResultSummary = "", SubagentResultCompleted, "done"
		}},
		{"completed without summary", func(s *Subagent) { s.LastResultStatus = SubagentResultCompleted }},
		{"completed with next step", func(s *Subagent) {
			s.LastResultStatus, s.LastResultSummary, s.LastResultNextStep = SubagentResultCompleted, "done", "more"
		}},
		{"incomplete without next step", func(s *Subagent) { s.LastResultStatus, s.LastResultSummary = SubagentResultIncomplete, "partial" }},
		{"failed without error", func(s *Subagent) { s.LastResultStatus = SubagentResultFailed }},
		{"error without failed outcome", func(s *Subagent) { s.LastResultError = "boom" }},
		{"delivery without assignment ID", func(s *Subagent) {
			s.ActiveTaskDelivery = &TaskDelivery{MainAgentTurnID: "turn_delivery"}
		}},
		{"delivery without main agent turn ID", func(s *Subagent) {
			s.ActiveTaskDelivery = &TaskDelivery{AssignmentID: "assignment_1"}
		}},
	}

	if err := ValidateSubagent(valid); err != nil {
		t.Fatalf("ValidateSubagent(valid): %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := CloneSubagent(valid)
			test.edit(&record)
			if err := ValidateSubagent(record); !errors.Is(err, ErrInvalidSubagent) {
				t.Fatalf("ValidateSubagent() error = %v, want ErrInvalidSubagent", err)
			}
		})
	}
}

func TestCloneSubagentDoesNotSharePendingMessagesOrTimestamps(t *testing.T) {
	record := testSubagent()
	closed := record.CreatedAt.Add(time.Minute)
	record.ClosedAt = &closed
	record.ActiveTaskDelivery = &TaskDelivery{MainAgentTurnID: "turn_delivery", AssignmentID: "assignment_1"}
	clone := CloneSubagent(record)
	clone.Pending[0].Content = "changed"
	*clone.ClosedAt = clone.ClosedAt.Add(time.Hour)
	clone.ActiveTaskDelivery.AssignmentID = "assignment_2"

	if record.Pending[0].Content != "continue" {
		t.Fatalf("pending content = %q, want continue", record.Pending[0].Content)
	}
	if record.ClosedAt.Equal(*clone.ClosedAt) {
		t.Fatal("closed timestamp shares pointer")
	}
	if record.ActiveTaskDelivery.AssignmentID != "assignment_1" {
		t.Fatalf("delivery assignment ID = %q, want assignment_1", record.ActiveTaskDelivery.AssignmentID)
	}
}

func TestCloneSubagentsDoesNotShareRecords(t *testing.T) {
	records := []Subagent{testSubagent()}
	clones := CloneSubagents(records)
	clones[0].Label = "changed"
	clones[0].Pending[0].Content = "changed"
	if records[0].Label != "research" || records[0].Pending[0].Content != "continue" {
		t.Fatalf("input changed through clone: %#v", records[0])
	}
}

func testSubagent() Subagent {
	now := time.Date(2026, time.July, 20, 0, 0, 0, 0, time.UTC)
	return Subagent{
		ID: "subagent_1", DisplayName: "Mira", Label: "research", MainAgentSessionID: "mainAgent_1", MainAgentTurnID: "turn_mainAgent_1",
		SubagentSessionID: "subagent_1", DefinitionName: "researcher", Provider: "openai", Model: "gpt-test",
		Status: "", LastSubagentTurnID: "turn_subagent_1", Version: 3,
		Pending:           []SubagentQueuedMessage{{ID: "queued_1", Content: "continue", CreatedAt: now}},
		ObservedMessageID: "message_1", ObservedVersion: 2, CreatedAt: now, UpdatedAt: now,
	}
}
