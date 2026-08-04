package agentcli

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

const runtimeLogEntryLimit = 2000

type runtimeLogEntry struct {
	sequence uint64
	text     string
}

// runtimeLogStore preserves managed console records for the Terminal UI while
// retaining stderr as the default output for every other surface. An attached
// interactive Terminal temporarily owns display of the records so they cannot
// corrupt its prompt or streaming renderer.
type runtimeLogStore struct {
	mu             sync.Mutex
	output         io.Writer
	entries        []runtimeLogEntry
	nextSequence   uint64
	terminalOwners int
	subscribers    map[chan runtimeLogEntry]struct{}
}

func projectLogger(config *LoggingConfig) (*slog.Logger, *runtimeLogStore) {
	if config == nil || !config.Enabled {
		return nil, nil
	}
	return managedRuntimeLogger(loggingLevel(config.Level))
}

func managedRuntimeLogger(level slog.Level) (*slog.Logger, *runtimeLogStore) {
	logs := newRuntimeLogStore(os.Stderr)
	return newRuntimeLogger(level, logs), logs
}

func newRuntimeLogger(level slog.Level, output io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: level}))
}

func newRuntimeLogStore(output io.Writer) *runtimeLogStore {
	return &runtimeLogStore{
		output:      output,
		entries:     make([]runtimeLogEntry, 0, runtimeLogEntryLimit),
		subscribers: make(map[chan runtimeLogEntry]struct{}),
	}
}

func (store *runtimeLogStore) Write(value []byte) (int, error) {
	if store == nil {
		return len(value), nil
	}
	entryText := string(append([]byte(nil), value...))

	store.mu.Lock()
	store.nextSequence++
	entry := runtimeLogEntry{sequence: store.nextSequence, text: entryText}
	store.entries = append(store.entries, entry)
	if overflow := len(store.entries) - runtimeLogEntryLimit; overflow > 0 {
		copy(store.entries, store.entries[overflow:])
		store.entries = store.entries[:runtimeLogEntryLimit]
	}
	for subscriber := range store.subscribers {
		select {
		case subscriber <- entry:
		default:
			// Keep the newest notification when a Terminal temporarily falls
			// behind. Its sequence gap makes the view rebuild from the bounded
			// snapshot instead of remaining stale at the dropped tail.
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- entry:
			default:
			}
		}
	}
	if store.terminalOwners > 0 || store.output == nil {
		store.mu.Unlock()
		return len(value), nil
	}
	n, err := store.output.Write(value)
	store.mu.Unlock()
	return n, err
}

func (store *runtimeLogStore) snapshot() []runtimeLogEntry {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]runtimeLogEntry(nil), store.entries...)
}

func (store *runtimeLogStore) attachTerminal() (<-chan runtimeLogEntry, func()) {
	updates := make(chan runtimeLogEntry, 256)
	if store == nil {
		close(updates)
		return updates, func() {}
	}
	store.mu.Lock()
	store.terminalOwners++
	store.subscribers[updates] = struct{}{}
	store.mu.Unlock()

	var once sync.Once
	return updates, func() {
		once.Do(func() {
			store.mu.Lock()
			delete(store.subscribers, updates)
			close(updates)
			if store.terminalOwners > 0 {
				store.terminalOwners--
			}
			store.mu.Unlock()
		})
	}
}

func loggingLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
