package agentcli

// withChildAgent marks an internally constructed child. It is deliberately
// private: only the manager may make a child that inherits project skills and
// caller tools while withholding management capabilities.
func withChildAgent() Option {
	return func(configuration *config) error {
		configuration.childAgent = true
		return nil
	}
}
