package agentruntime

import (
	"errors"
	"testing"
	"time"
)

type deterministicIDGenerator struct {
	ids      map[string]string
	prefixes []string
}

func (g *deterministicIDGenerator) NewID(prefix string) (string, error) {
	g.prefixes = append(g.prefixes, prefix)
	return g.ids[prefix], nil
}

func TestNormalizeRequestRejectsInvalidCallerInput(t *testing.T) {
	valid := Request{
		SessionID: "session-1",
		Message:   Message{Type: MessageTypeUser, Content: "hello"},
	}

	tests := []struct {
		name    string
		request Request
	}{
		{name: "missing session ID", request: func() Request { r := valid; r.SessionID = ""; return r }()},
		{name: "empty user content", request: func() Request { r := valid; r.Message.Content = ""; return r }()},
		{name: "whitespace user content", request: func() Request { r := valid; r.Message.Content = " \t\n "; return r }()},
		{name: "non-user message", request: func() Request { r := valid; r.Message.Type = MessageTypeAssistant; return r }()},
		{name: "conflicting message session", request: func() Request { r := valid; r.Message.SessionID = "other"; return r }()},
		{name: "conflicting message turn", request: func() Request { r := valid; r.TurnID = "turn-1"; r.Message.TurnID = "turn-2"; return r }()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeRequest(test.request, &deterministicIDGenerator{ids: map[string]string{"turn_": "turn-x", "msg_": "msg-x"}})
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("normalizeRequest() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestNormalizeRequestPreservesCallerTurnAndFillsMessage(t *testing.T) {
	generator := &deterministicIDGenerator{ids: map[string]string{"msg_": "msg-generated"}}
	request := Request{
		SessionID: "session-1",
		TurnID:    "turn-caller",
		Message: Message{
			Type:      MessageTypeUser,
			Content:   "hello",
			CreatedAt: time.Date(2020, time.January, 1, 0, 0, 0, 0, time.FixedZone("UTC+7", 7*60*60)),
		},
	}

	normalized, err := normalizeRequest(request, generator)
	if err != nil {
		t.Fatalf("normalizeRequest() error = %v", err)
	}
	if normalized.TurnID != "turn-caller" {
		t.Fatalf("TurnID = %q, want caller value", normalized.TurnID)
	}
	if normalized.Message.ID != "msg-generated" || normalized.Message.SessionID != request.SessionID || normalized.Message.TurnID != request.TurnID {
		t.Fatalf("message = %#v, want generated ID and request identity", normalized.Message)
	}
	if normalized.Message.CreatedAt.Location() != time.UTC || !normalized.Message.CreatedAt.Equal(request.Message.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want original instant normalized to UTC", normalized.Message.CreatedAt)
	}
	if len(generator.prefixes) != 1 || generator.prefixes[0] != "msg_" {
		t.Fatalf("ID prefixes = %v, want [msg_]", generator.prefixes)
	}
	if request.Message.ID != "" || request.Message.SessionID != "" || request.Message.TurnID != "" {
		t.Fatalf("normalizeRequest mutated input message: %#v", request.Message)
	}
}

func TestNormalizeRequestGeneratesTurnAndMessageIDs(t *testing.T) {
	generator := &deterministicIDGenerator{ids: map[string]string{"turn_": "turn-generated", "msg_": "msg-generated"}}
	normalized, err := normalizeRequest(Request{
		SessionID: "session-1",
		Message:   Message{Type: MessageTypeUser, Content: "hello"},
	}, generator)
	if err != nil {
		t.Fatalf("normalizeRequest() error = %v", err)
	}
	if normalized.TurnID != "turn-generated" || normalized.Message.ID != "msg-generated" {
		t.Fatalf("normalized IDs = (%q, %q), want generated IDs", normalized.TurnID, normalized.Message.ID)
	}
	if got, want := generator.prefixes, []string{"turn_", "msg_"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ID prefixes = %v, want %v", got, want)
	}
	if normalized.Message.CreatedAt.IsZero() || normalized.Message.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt = %v, want non-zero UTC time", normalized.Message.CreatedAt)
	}
}

func TestNormalizeRequestAcceptsTrustedRuntimeEvent(t *testing.T) {
	normalized, err := normalizeRequest(Request{
		SessionID: "session-1",
		Message:   Message{Type: MessageTypeRuntimeEvent, Content: "callback"},
	}, &deterministicIDGenerator{ids: map[string]string{"turn_": "turn-generated", "msg_": "msg-generated"}})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Message.Type != MessageTypeRuntimeEvent || normalized.Message.Content != "callback" {
		t.Fatalf("normalized runtime event = %#v", normalized.Message)
	}
}
