package agentcli

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

func projectLogger(config *LoggingConfig) *slog.Logger {
	if config == nil || !config.Enabled {
		return nil
	}
	return newRuntimeLogger(loggingLevel(config.Level), os.Stderr)
}

func newRuntimeLogger(level slog.Level, output io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: level}))
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
