package provider

import "testing"

func TestStateReturnsImmutableEventHistory(t *testing.T) {
	initial := EmptyState()
	first := StreamEvent{Type: ContentReceived, Content: "hello"}
	second := StreamEvent{Type: ReasoningReceived, Reasoning: "think"}

	withFirst := State(initial, first)
	withSecond := State(withFirst, second)

	if got := Events(initial); len(got) != 0 {
		t.Fatalf("initial state changed: %#v", got)
	}
	if got := Events(withFirst); len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("first state = %#v", got)
	}
	if got := Events(withSecond); len(got) != 2 || got[1].Reasoning != "think" {
		t.Fatalf("second state = %#v", got)
	}
}

func TestEventsReturnsDefensiveCopy(t *testing.T) {
	state := State(EmptyState(), StreamEvent{Type: ContentReceived, Content: "hello"})
	history := Events(state)
	history[0].Content = "mutated"

	if got := Events(state); got[0].Content != "hello" {
		t.Fatalf("state history was mutated through copy: %#v", got)
	}
}
