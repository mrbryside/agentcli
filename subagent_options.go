package agentcli

import (
	"log/slog"

	langfuseobs "github.com/mrbryside/agentcli/observability/langfuse"
)

// withSubagentAgent marks an internally constructed subagent. It is deliberately
// private: only the manager may make a subagent that inherits project skills and
// caller tools while withholding management capabilities.
func withSubagentAgent() Option {
	return func(configuration *config) error {
		configuration.subagentAgent = true
		return nil
	}
}

// withSharedLangfuse reuses the main Agent's exporter for a subagent. The main
// remains the sole owner responsible for flushing and shutting it down.
func withSharedLangfuse(client *langfuseobs.Client) Option {
	return func(configuration *config) error {
		configuration.langfuse = client
		return nil
	}
}

// withSharedLogger keeps main-agent and subagent lifecycle records on the same handler.
func withSharedLogger(logger *slog.Logger) Option {
	return func(configuration *config) error {
		configuration.logger = logger
		return nil
	}
}
