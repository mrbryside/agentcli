package agentcli

import langfuseobs "github.com/mrbryside/agentcli/observability/langfuse"

// withChildAgent marks an internally constructed child. It is deliberately
// private: only the manager may make a child that inherits project skills and
// caller tools while withholding management capabilities.
func withChildAgent() Option {
	return func(configuration *config) error {
		configuration.childAgent = true
		return nil
	}
}

// withSharedLangfuse reuses the root Agent's exporter for a child. The root
// remains the sole owner responsible for flushing and shutting it down.
func withSharedLangfuse(client *langfuseobs.Client) Option {
	return func(configuration *config) error {
		configuration.langfuse = client
		return nil
	}
}
