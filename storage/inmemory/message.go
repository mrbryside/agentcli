// Package inmemory provides a synchronized in-memory MessageStorage.
package inmemory

import (
	"context"
	"fmt"
	"sync"

	"harness-api/storage"
)

// MessageStorage stores ordered transcripts in process memory.
type MessageStorage struct {
	mu       sync.RWMutex
	sessions map[string]*sessionEntry
}

type sessionEntry struct {
	mu       sync.RWMutex
	messages []storage.Message
	ids      map[string]struct{}
}

// NewMessageStorage returns an empty, concurrency-safe message store.
func NewMessageStorage() *MessageStorage {
	return &MessageStorage{sessions: make(map[string]*sessionEntry)}
}

// Append validates and atomically appends one same-session batch in argument
// order. It retains copies of every appended message.
func (s *MessageStorage) Append(ctx context.Context, messages ...storage.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}

	sessionID, err := validateAppend(messages)
	if err != nil {
		return err
	}
	entry := s.session(sessionID)

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, message := range messages {
		if _, exists := entry.ids[message.ID]; exists {
			return duplicateMessageID(message.ID)
		}
	}

	entry.messages = append(entry.messages, storage.CloneMessages(messages)...)
	for _, message := range messages {
		entry.ids[message.ID] = struct{}{}
	}
	return nil
}

// List returns copies of the messages in a session's transcript, in append
// order. Unknown sessions have an empty transcript.
func (s *MessageStorage) List(ctx context.Context, sessionID string) ([]storage.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry := s.existingSession(sessionID)
	if entry == nil {
		return nil, nil
	}

	entry.mu.RLock()
	messages := storage.CloneMessages(entry.messages)
	entry.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// TurnExists reports whether a transcript contains a message from turnID.
func (s *MessageStorage) TurnExists(ctx context.Context, sessionID, turnID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	entry := s.existingSession(sessionID)
	if entry == nil {
		return false, nil
	}

	entry.mu.RLock()
	defer entry.mu.RUnlock()
	for _, message := range entry.messages {
		if message.TurnID == turnID {
			return true, nil
		}
	}
	return false, nil
}

func (s *MessageStorage) session(sessionID string) *sessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.sessions[sessionID]; entry != nil {
		return entry
	}
	entry := &sessionEntry{ids: make(map[string]struct{})}
	s.sessions[sessionID] = entry
	return entry
}

func (s *MessageStorage) existingSession(sessionID string) *sessionEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionID]
}

func validateAppend(messages []storage.Message) (string, error) {
	sessionID := messages[0].SessionID
	seenIDs := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		if err := storage.ValidateMessage(message); err != nil {
			return "", err
		}
		if message.SessionID != sessionID {
			return "", fmt.Errorf("%w: all messages in an append must share a session ID", storage.ErrInvalidMessage)
		}
		if _, exists := seenIDs[message.ID]; exists {
			return "", duplicateMessageID(message.ID)
		}
		seenIDs[message.ID] = struct{}{}
	}
	return sessionID, nil
}

func duplicateMessageID(id string) error {
	return fmt.Errorf("%w: %q", storage.ErrDuplicateMessageID, id)
}
