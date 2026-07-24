package agentcli

import (
	"errors"
	"fmt"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
	"github.com/mrbryside/agentcli/toolexecution"
)

const defaultChannelBuffer = 64
const defaultToolWorkers = 4
const defaultMaxSubagents = 4

// defaultCompletionRepairLimit bounds provider retries when a completion
// guard requires a finalizer or semantic outcome tool. A bounded retry keeps
// a non-compliant provider from consuming the entire run indefinitely while
// allowing compatible providers more than one opportunity to emit the tool.
const defaultCompletionRepairLimit = 3

// Option configures an Agent created by New.
type Option func(*config) error

type config struct {
	model                   agentruntime.Model
	systemPrompts           []string
	projectRoot             string
	permissionMode          permission.Mode
	permissionPolicy        permission.Policy
	nonInteractive          bool
	toolWorkers             int
	channelBuffer           int
	messages                storage.MessageStorage
	permissions             storage.PermissionStorage
	confirmations           storage.ConfirmationStorage
	tools                   []toolexecution.Tool
	project                 *Project
	skillReload             SkillReloadPolicy
	subagents               storage.SubagentStorage
	maxSubagents            int
	childAgent              bool
	contextReminderProvider agentruntime.ContextReminderProvider
}

func defaultConfig(projectRoot string) config {
	return config{
		projectRoot:      projectRoot,
		permissionMode:   permission.Default,
		permissionPolicy: permission.Policy{Mode: permission.Default},
		toolWorkers:      defaultToolWorkers,
		channelBuffer:    defaultChannelBuffer,
		messages:         inmemory.NewMessageStorage(),
		permissions:      inmemory.NewPermissionStorage(),
		confirmations:    inmemory.NewConfirmationStorage(),
		skillReload:      DefaultSkillReloadPolicy(),
	}
}

// WithSkillReloadPolicy controls when load_skill returns the full instructions
// again to refresh an old skill near the latest conversation messages.
func WithSkillReloadPolicy(policy SkillReloadPolicy) Option {
	return func(configuration *config) error {
		if err := policy.Validate(); err != nil {
			return err
		}
		configuration.skillReload = policy
		return nil
	}
}

func (configuration config) validate() error {
	if configuration.channelBuffer <= 0 {
		return errors.New("channel buffer must be positive")
	}
	if configuration.toolWorkers <= 0 {
		return errors.New("tool workers must be positive")
	}
	if configuration.messages == nil {
		return errors.New("message storage is required")
	}
	if configuration.permissions == nil {
		return errors.New("permission storage is required")
	}
	if configuration.confirmations == nil {
		return errors.New("confirmation storage is required")
	}
	if configuration.maxSubagents < 0 {
		return errors.New("maximum subagents cannot be negative")
	}
	if configuration.maxSubagents > 0 && configuration.subagents == nil && configuration.project != nil && len(configuration.project.subagents) != 0 && !configuration.childAgent {
		return errors.New("subagent storage is required")
	}
	if err := configuration.skillReload.Validate(); err != nil {
		return err
	}
	if !isPermissionMode(configuration.permissionMode) {
		return fmt.Errorf("unknown permission mode %q", configuration.permissionMode)
	}
	if configuration.permissionPolicy.Mode != configuration.permissionMode {
		return errors.New("permission policy mode must match permission mode")
	}
	return nil
}

func isPermissionMode(mode permission.Mode) bool {
	return permission.IsValidMode(mode)
}

// WithModel supplies the provider-backed model required by New.
func WithModel(model agentruntime.Model) Option {
	return func(configuration *config) error {
		configuration.model = model
		return nil
	}
}

// WithSystemPrompt supplies application instructions to every provider round
// without persisting them in the session transcript.
func WithSystemPrompt(prompt string) Option {
	return func(configuration *config) error {
		configuration.systemPrompts = append(configuration.systemPrompts, prompt)
		return nil
	}
}

// WithProjectRoot identifies the project for permission decisions. In
// particular, AllowProject grants apply only to this project identity.
func WithProjectRoot(root string) Option {
	return func(configuration *config) error {
		if root == "" {
			return errors.New("project root is required")
		}
		configuration.projectRoot = root
		return nil
	}
}

// WithPermissionMode selects the same mode for the runtime and executor.
// When combined with an explicit-mode WithPermissionPolicy, the last option
// wins. A policy without a mode inherits the mode selected so far.
func WithPermissionMode(mode permission.Mode) Option {
	return func(configuration *config) error {
		if !isPermissionMode(mode) {
			return fmt.Errorf("unknown permission mode %q", mode)
		}
		configuration.permissionMode = mode
		configuration.permissionPolicy.Mode = mode
		return nil
	}
}

// WithPermissionPolicy supplies executor admission rules. If policy.Mode is
// empty it inherits the currently selected mode; otherwise it selects that
// mode for both the runtime and executor. Consequently, an explicit mode in
// the last permission option wins regardless of option order.
func WithPermissionPolicy(policy permission.Policy) Option {
	return func(configuration *config) error {
		if policy.Mode == "" {
			policy.Mode = configuration.permissionMode
		}
		if !isPermissionMode(policy.Mode) {
			return fmt.Errorf("unknown permission mode %q", policy.Mode)
		}
		configuration.permissionMode = policy.Mode
		configuration.permissionPolicy = policy
		return nil
	}
}

// WithNonInteractive denies permission prompts and declines confirmations
// rather than waiting for a UI decision. It is useful for unattended runs.
func WithNonInteractive(nonInteractive bool) Option {
	return func(configuration *config) error {
		configuration.nonInteractive = nonInteractive
		return nil
	}
}

// WithToolWorkers sets the executor worker count.
func WithToolWorkers(count int) Option {
	return func(configuration *config) error {
		configuration.toolWorkers = count
		return nil
	}
}

// WithChannelBuffer sets the shared tool and permission channel capacity.
func WithChannelBuffer(size int) Option {
	return func(configuration *config) error {
		configuration.channelBuffer = size
		return nil
	}
}

// WithMessageStorage replaces the default in-memory transcript store.
func WithMessageStorage(messages storage.MessageStorage) Option {
	return func(configuration *config) error {
		configuration.messages = messages
		return nil
	}
}

// WithPermissionStorage replaces the default in-memory permission store.
func WithPermissionStorage(permissions storage.PermissionStorage) Option {
	return func(configuration *config) error {
		configuration.permissions = permissions
		return nil
	}
}

// WithConfirmationStorage replaces the default in-memory Yes/No confirmation
// store. It is independent from permission storage and permission modes.
func WithConfirmationStorage(confirmations storage.ConfirmationStorage) Option {
	return func(configuration *config) error {
		if confirmations == nil {
			return errors.New("confirmation storage is required")
		}
		configuration.confirmations = confirmations
		return nil
	}
}

// WithSubagentStorage replaces the child-session relationship store used by
// a project-backed root Agent. Child Agents never create a manager of their
// own, even when this option is present.
func WithSubagentStorage(subagents storage.SubagentStorage) Option {
	return func(configuration *config) error {
		if subagents == nil {
			return errors.New("subagent storage is required")
		}
		configuration.subagents = subagents
		return nil
	}
}

// WithMaxSubagents bounds the number of non-closed child instances each
// parent session may keep open. The default is applied only for projects that
// define subagents.
func WithMaxSubagents(maximum int) Option {
	return func(configuration *config) error {
		if maximum <= 0 {
			return errors.New("maximum subagents must be positive")
		}
		configuration.maxSubagents = maximum
		return nil
	}
}

// WithTool registers a caller-provided executable tool.
func WithTool(tool toolexecution.Tool) Option {
	return func(configuration *config) error {
		configuration.tools = append(configuration.tools, tool)
		return nil
	}
}

// WithContextReminderProvider supplies trusted, ephemeral context for each
// provider round. Its values are never written to MessageStorage. A
// project-backed root Agent composes this provider with its active-subagent
// reminder rather than replacing it.
func WithContextReminderProvider(provider agentruntime.ContextReminderProvider) Option {
	return func(configuration *config) error {
		configuration.contextReminderProvider = provider
		return nil
	}
}
