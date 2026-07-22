package agentruntime

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/mrbryside/agentcli/storage"
)

// Request starts one agent turn with a required session and either a human
// user message or a trusted runtime event.
type Request struct {
	SessionID string
	TurnID    string
	Message   Message
}

// IDGenerator creates a collision-resistant identifier with the requested
// namespace prefix.
type IDGenerator interface {
	NewID(prefix string) (string, error)
}

// cryptoIDGenerator produces IDs from 128 bits of cryptographically secure
// randomness.
type cryptoIDGenerator struct{}

func (cryptoIDGenerator) NewID(prefix string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("read random identifier bytes: %w", err)
	}
	return prefix + hex.EncodeToString(bytes), nil
}

// normalizeRequest validates caller input and creates the values persisted for
// the turn. It never retains or mutates the caller's message.
func normalizeRequest(request Request, generator IDGenerator) (Request, error) {
	if request.SessionID == "" {
		return Request{}, invalidRequest("session ID is required")
	}
	if request.Message.Type != MessageTypeUser && request.Message.Type != MessageTypeRuntimeEvent {
		return Request{}, invalidRequest("message type must be user or runtime_event")
	}
	if request.Message.SessionID != "" && request.Message.SessionID != request.SessionID {
		return Request{}, invalidRequest("message session ID conflicts with request session ID")
	}

	if generator == nil {
		generator = cryptoIDGenerator{}
	}
	turnID := request.TurnID
	if turnID == "" {
		var err error
		turnID, err = generator.NewID("turn_")
		if err != nil {
			return Request{}, fmt.Errorf("generate turn ID: %w", err)
		}
		if turnID == "" {
			return Request{}, invalidRequest("generated turn ID is empty")
		}
	}
	if request.Message.TurnID != "" && request.Message.TurnID != turnID {
		return Request{}, invalidRequest("message turn ID conflicts with request turn ID")
	}

	message := storage.CloneMessage(request.Message)
	message.SessionID = request.SessionID
	message.TurnID = turnID
	if message.ID == "" {
		var err error
		message.ID, err = generator.NewID("msg_")
		if err != nil {
			return Request{}, fmt.Errorf("generate message ID: %w", err)
		}
		if message.ID == "" {
			return Request{}, invalidRequest("generated message ID is empty")
		}
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	} else {
		message.CreatedAt = message.CreatedAt.UTC()
	}
	if err := storage.ValidateMessage(message); err != nil {
		return Request{}, fmt.Errorf("%w: message: %v", ErrInvalidRequest, err)
	}

	return Request{SessionID: request.SessionID, TurnID: turnID, Message: message}, nil
}

func invalidRequest(format string, arguments ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidRequest}, arguments...)...)
}
