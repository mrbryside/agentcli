package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	openaiadapter "github.com/mrbryside/agentcli/agentruntime/modeladapter/openai"
	"github.com/mrbryside/agentcli/permission"
	provideropenai "github.com/mrbryside/agentcli/provider/openai"
	"github.com/mrbryside/agentcli/toolexecution"

	"gopkg.in/yaml.v3"
)

const (
	maxProjectFileSize = 1 << 20
	// skillLoaderToolName is reserved for the framework's progressive skill
	// loader. Applications must not register a custom tool with this name.
	skillLoaderToolName = toolexecution.SkillLoaderToolName
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// projectConfig is loaded from .agentcli/config.yaml. Main-agent identity and
// capabilities live in .agentcli/MAIN.md.
type projectConfig struct {
	PermissionMode permission.Mode           `yaml:"permission_mode"`
	Providers      map[string]providerConfig `yaml:"providers"`
	Compaction     *compactionConfig         `yaml:"compaction"`
	Logging        *loggingConfig            `yaml:"logging"`
}

// loggingConfig controls structured runtime lifecycle logs. Records normally
// go to stderr; an interactive RunTerminal captures them for its log view.
// Omitting the section disables runtime logging. When the section is present,
// enabled defaults to true and level defaults to info.
type loggingConfig struct {
	Enabled bool   `yaml:"enabled"`
	Level   string `yaml:"level"`
}

// UnmarshalYAML applies useful opt-in defaults while keeping the section
// strict even though custom YAML unmarshalling bypasses Decoder.KnownFields.
func (config *loggingConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return errors.New("logging must be a mapping")
	}
	for index := 0; index < len(value.Content); index += 2 {
		key := value.Content[index].Value
		if key != "enabled" && key != "level" {
			return fmt.Errorf("field %s not found in logging config", key)
		}
	}
	type rawLoggingConfig loggingConfig
	decoded := rawLoggingConfig{Enabled: true, Level: "info"}
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*config = loggingConfig(decoded)
	return nil
}

// compactionConfig selects the project provider profile and model used to
// compact an agent transcript. Model limits belong to providerConfig so every
// main, subagent, and compaction model uses its own provider profile's metadata.
// A nil value means compaction is disabled; when the section is present Auto
// defaults to true.
type compactionConfig struct {
	Auto     bool   `yaml:"auto"`
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// UnmarshalYAML makes an explicitly configured compaction section opt in by
// default while preserving auto: false as an explicit disable. Custom YAML
// unmarshalling bypasses Decoder.KnownFields, so keep this section strict.
func (config *compactionConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return errors.New("compaction must be a mapping")
	}
	for index := 0; index < len(value.Content); index += 2 {
		key := value.Content[index].Value
		if key != "auto" && key != "provider" && key != "model" {
			return fmt.Errorf("field %s not found in compaction config", key)
		}
	}
	type rawCompactionConfig compactionConfig
	decoded := rawCompactionConfig{Auto: true}
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*config = compactionConfig(decoded)
	return nil
}

// providerType selects the protocol adapter used by a named connection
// profile. Map keys under providers are application-defined aliases.
type providerType string

const (
	// providerTypeOpenAI selects the OpenAI-compatible chat-completions adapter.
	providerTypeOpenAI providerType = "openai"
)

type providerConfig struct {
	Type           providerType `yaml:"type"`
	URL            string       `yaml:"url"`
	APIKey         string       `yaml:"api_key"`
	RequestTimeout string       `yaml:"request_timeout"`
	// Models contains optional exact-name overrides. It does not restrict which
	// model names agents may select.
	Models map[string]providerModelConfig `yaml:"models"`
}

// providerModelConfig contains capability and request overrides for one exact
// model name within a provider profile.
type providerModelConfig struct {
	// Reasoning controls the Qwen-compatible enable_thinking switch. Nil
	// preserves the provider default; use ExtraBody for other wire formats.
	Reasoning *bool `yaml:"reasoning"`
	// ExtraBody contains provider-specific top-level JSON fields merged into
	// chat-completions requests for this model. Values override standard
	// request fields with the same names.
	ExtraBody map[string]any `yaml:"extra_body"`
	// Optional model-limit overrides applied only when this exact model name
	// is selected. Zero values use metadata discovery and defaults.
	ContextWindowTokens int `yaml:"context_window_tokens"`
	MaxOutputTokens     int `yaml:"max_output_tokens"`
}

// skill is one .agentcli/skill/<name>/SKILL.md file. Only name and
// description are YAML metadata; Instructions is the Markdown body.
type skill struct {
	Name         string
	Description  string
	Instructions string
	Path         string
}

// Project is an immutable snapshot of project instructions, skills, and
// provider configuration.
type Project struct {
	root          string
	main          agentDefinition
	config        projectConfig
	skills        map[string]skill // skills available to this main-agent/subagent view
	allSkills     map[string]skill // complete project catalog for subagent allowlists
	subagents     map[string]agentDefinition
	providerName  string
	modelName     string
	compaction    *compactionConfig
	modelMetadata map[projectModelReference]agentruntime.ModelMetadata
	toolNames     []string
	restrictTools bool
	timeout       time.Duration
}

// LoadProject reads .agentcli/MAIN.md, .agentcli/config.yaml, and the
// configured skill and subagent definitions under root.
func LoadProject(root string) (*Project, error) {
	return LoadProjectContext(context.Background(), root)
}

// LoadProjectContext loads and validates one project. When automatic
// compaction needs metadata that is not configured, discovery checks the
// provider /models endpoint before falling back to models.dev under ctx, then
// uses the project defaults when neither source provides valid limits.
func LoadProjectContext(ctx context.Context, root string) (*Project, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("project root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	configPath := filepath.Join(absoluteRoot, ".agentcli", "config.yaml")
	configBytes, err := readProjectFile(configPath, true)
	if err != nil {
		return nil, fmt.Errorf("load project config: %w", err)
	}
	var config projectConfig
	if err := decodeYAML(configBytes, &config); err != nil {
		return nil, fmt.Errorf("decode %s: %w", configPath, err)
	}
	expandProjectConfig(&config)
	if config.PermissionMode == "" {
		config.PermissionMode = permission.Default
	}

	mainPath := filepath.Join(absoluteRoot, ".agentcli", "MAIN.md")
	mainBytes, err := readProjectFile(mainPath, true)
	if err != nil {
		return nil, fmt.Errorf("load .agentcli/MAIN.md: %w", err)
	}
	mainDefinition, err := parseMainDefinition(mainPath, mainBytes)
	if err != nil {
		return nil, err
	}
	providerName, modelName, providerConfig, timeout, err := validateProjectConfig(config, mainDefinition)
	if err != nil {
		return nil, fmt.Errorf("validate %s: %w", configPath, err)
	}
	allSkills, err := loadSkills(filepath.Join(absoluteRoot, ".agentcli", "skill"))
	if err != nil {
		return nil, err
	}
	rootSkills, err := selectProjectSkills(allSkills, mainDefinition.Skills)
	if err != nil {
		return nil, fmt.Errorf("validate %s skills: %w", mainPath, err)
	}
	subagents, err := loadSubagentDefinitions(filepath.Join(absoluteRoot, ".agentcli", "agent"), config.Providers, allSkills)
	if err != nil {
		return nil, err
	}
	if err := validateProjectTaskAgents(subagents); err != nil {
		return nil, err
	}
	config.Providers[providerName] = providerConfig
	project := &Project{
		root: absoluteRoot, main: mainDefinition, config: config,
		skills: rootSkills, allSkills: allSkills, subagents: subagents,
		providerName: providerName, modelName: modelName,
		compaction:    cloneCompactionConfig(config.Compaction),
		modelMetadata: make(map[projectModelReference]agentruntime.ModelMetadata),
		toolNames:     append([]string{}, mainDefinition.Tools...), restrictTools: true,
		timeout: timeout,
	}
	if project.compaction != nil && project.compaction.Auto {
		if err := project.resolveRequiredModelMetadata(ctx); err != nil {
			return nil, fmt.Errorf("resolve project model metadata: %w", err)
		}
	}
	return project, nil
}

// WithProject applies a loaded project to Agent.New. It selects the configured
// model, project permission identity, permission mode, system prompts, and
// skill loader. A later Agent option may override scalar values.
func WithProject(project *Project) Option {
	return func(configuration *config) error {
		if project == nil {
			return errors.New("project is required")
		}
		model, err := project.model()
		if err != nil {
			return err
		}
		configuration.model = model
		configuration.projectRoot = project.root
		configuration.systemPrompts = append(configuration.systemPrompts, project.systemPrompts()...)
		configuration.project = project
		configuration.permissionMode = project.permissionMode()
		configuration.permissionPolicy.Mode = project.permissionMode()
		configuration.logger, configuration.runtimeLogs = projectLogger(project.config.Logging)
		if project.compaction != nil && project.compaction.Auto {
			compactionModel, err := project.compactionModel()
			if err != nil {
				return fmt.Errorf("resolve compaction model: %w", err)
			}
			configuration.compactionModel = compactionModel
		}
		return nil
	}
}

// model constructs the configured OpenAI-compatible model adapter.
func (project *Project) model() (agentruntime.Model, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}
	return project.modelFor(project.providerName, project.modelName)
}

// modelFor constructs a model for a named project provider profile and model.
// The profile's type selects the protocol adapter; URL, credential, and
// timeout always remain in project config.
func (project *Project) modelFor(providerName, model string) (agentruntime.Model, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" {
		return nil, errors.New("provider is required")
	}
	if model == "" {
		return nil, errors.New("model is required")
	}
	providerConfig, found := project.config.Providers[providerName]
	if !found {
		return nil, fmt.Errorf("provider %q is not configured", providerName)
	}
	timeout, err := validateProviderConfig(providerName, providerConfig)
	if err != nil {
		return nil, err
	}
	switch providerConfig.Type {
	case providerTypeOpenAI:
		modelConfig := providerConfig.Models[model]
		adapterConfig := openaiadapter.Config{
			Provider:  providerName,
			Model:     model,
			Reasoning: cloneBool(modelConfig.Reasoning),
		}
		metadata, hasMetadata := project.modelMetadata[projectModelReference{provider: providerName, model: model}]
		if !hasMetadata {
			var metadataErr error
			metadata, hasMetadata, metadataErr = configuredProviderMetadata(providerName, model, providerConfig)
			if metadataErr != nil {
				return nil, metadataErr
			}
		}
		if hasMetadata {
			adapterConfig.MetadataResolver = func(string) (agentruntime.ModelMetadata, error) {
				return metadata, nil
			}
		}
		return openaiadapter.New(
			provideropenai.NewProvider(provideropenai.Config{
				URL:       providerConfig.URL,
				APIKey:    providerConfig.APIKey,
				ExtraBody: modelConfig.ExtraBody,
				Timeout:   timeout,
			}),
			adapterConfig,
		), nil
	default:
		return nil, unsupportedProviderType(providerName, providerConfig.Type)
	}
}

// compactionSettings returns the configured transcript-compaction policy. Its
// boolean result reports whether the compaction section is present.
func (project *Project) compactionSettings() (compactionConfig, bool) {
	if project == nil || project.compaction == nil {
		return compactionConfig{}, false
	}
	return *cloneCompactionConfig(project.compaction), true
}

// compactionModel constructs the configured compaction model through the same
// provider-profile factory used for agent models.
func (project *Project) compactionModel() (agentruntime.Model, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}
	if project.compaction == nil || !project.compaction.Auto {
		return nil, errors.New("compaction is disabled")
	}
	return project.modelFor(project.compaction.Provider, project.compaction.Model)
}

// configuredToolNames returns the main agent's custom-tool allowlist.
func (project *Project) configuredToolNames() []string {
	if project == nil || !project.restrictTools {
		return nil
	}
	return append([]string{}, project.toolNames...)
}

func (project *Project) permissionMode() permission.Mode {
	if project == nil {
		return ""
	}
	return project.config.PermissionMode
}

// sortedSkills returns discovered skills in stable name order.
func (project *Project) sortedSkills() []skill {
	if project == nil {
		return nil
	}
	names := make([]string, 0, len(project.skills))
	for name := range project.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	skills := make([]skill, len(names))
	for index, name := range names {
		skills[index] = project.skills[name]
	}
	return skills
}

// mainAgentDefinition returns the definition loaded from .agentcli/MAIN.md.
func (project *Project) mainAgentDefinition() agentDefinition {
	if project == nil {
		return agentDefinition{}
	}
	return cloneSubagentDefinition(project.main)
}

// subagentDefinitions returns complete definitions in stable name order.
func (project *Project) subagentDefinitions() []agentDefinition {
	if project == nil {
		return nil
	}
	return sortedSubagentDefinitions(project.subagents)
}

// systemPrompts returns the framework prompt, optional focused skill and
// subagent prompts, and MAIN.md instructions as separate ordered system
// messages. Full skill bodies are loaded only through load_skill after the
// model selects a skill by description or follows an explicit load requirement.
func (project *Project) systemPrompts() []string {
	if project == nil {
		return nil
	}
	prompts := make([]string, 0, 4)
	if frameworkPrompt := project.mainAgentSystemPrompt(); frameworkPrompt != "" {
		prompts = append(prompts, frameworkPrompt)
	}
	if len(project.skills) != 0 {
		prompts = append(prompts, "# Skills\n\n"+project.skillDiscoveryPrompt())
	}
	if len(project.subagents) != 0 {
		prompts = append(prompts,
			"# Subagents\n\n"+
				mainAgentSubagentToolPrompt+
				"\n\n"+project.subagentDiscoveryPrompt(),
		)
	}
	if instructions := strings.TrimSpace(project.main.Instructions); instructions != "" {
		prompts = append(prompts, "# Main agent instructions\n\n"+instructions)
	}
	return prompts
}

func (project *Project) subagentDiscoveryPrompt() string {
	var prompt strings.Builder
	prompt.WriteString(`Choose a configured task agent whose description directly matches focused work that benefits from specialization or isolation. Otherwise answer directly. The task tool accepts exactly the agents below.

<available_task_agents>
`)
	for _, definition := range project.subagentDefinitions() {
		fmt.Fprintf(&prompt, "  <task_agent>\n    <name>%s</name>\n    <description>%s</description>\n  </task_agent>\n", html.EscapeString(definition.Name), html.EscapeString(definition.Description))
	}
	prompt.WriteString("</available_task_agents>")
	return prompt.String()
}

func validateProjectTaskAgents(definitions map[string]agentDefinition) error {
	for _, definition := range definitions {
		for _, toolName := range definition.Tools {
			if toolName == toolexecution.TaskToolName {
				return fmt.Errorf("subagent %q cannot list main-agent task tool %q", definition.Name, toolName)
			}
		}
	}
	return nil
}

func (project *Project) skillDiscoveryPrompt() string {
	var prompt strings.Builder
	prompt.WriteString(`The available skills are listed in available_skills. A description helps you choose a skill; load_skill provides that skill's full instructions.

<skill_rules>
## Choose

Load a skill when an applicable instruction requires it, its description directly
matches the task you are about to perform, or the user asks to read its full
instructions. Do not load a skill merely to list or describe available skills.
Do not load an unrelated skill as a substitute for a missing capability.
The only valid load_skill names are exact <name> values in the current
<available_skills>. Never copy a skill name from conversation history, an older
turn, a task agent, a tool name, or user wording unless that exact name is also
listed in the current <available_skills>.

When another instruction requires a skill, load that skill before the action or
answer it governs. Explicit requirements take precedence over description
similarity.

## Load once per turn

A <turn_start> message marks a new turn. Each skill may be loaded once during
that turn. Later model requests and tool results without another <turn_start>
remain in the same turn.

After load_skill returns status=loaded and name=<skill>, that exact skill is
loaded for this turn. Do not load it again until a new <turn_start>. A different
skill is separate and may be loaded for its own valid reason.

## Read the result

One load_skill call loads only the skill named in that call.

- instructions contains the full skill body when it is returned.
- instructions_in_context=true means the full body is already available in the
  conversation. The load still succeeded; do not call load_skill again.
- tools_unchanged=true means loading the skill did not add, remove, or enable
  tools. The tools listed with the current request are available now and are
  the authoritative tool list. If a tool name is listed, do not claim it is
  missing or ask the user to enable it.
- Loading a skill only makes its instructions available. It does not require an
  action. Follow the loaded instructions and the current request to decide what
  to do next.

`)
	skills := project.sortedSkills()
	if len(skills) != 0 {
		first := html.EscapeString(skills[0].Name)
		prompt.WriteString("Examples using only names from the current <available_skills>:\n\n")
		fmt.Fprintf(&prompt, "- status=loaded, name=%s: %s is loaded; do not load it again in this turn.\n", first, first)
		fmt.Fprintf(&prompt, "- status=loaded, name=%s, instructions_in_context=true: loading succeeded and its full instructions are already in the conversation.\n", first)
		if len(skills) > 1 {
			second := html.EscapeString(skills[1].Name)
			fmt.Fprintf(&prompt, "- If %s is loaded and %s later has a separate valid trigger, call load_skill once with name=%s.\n", first, second, second)
		}
	}
	prompt.WriteString(`
</skill_rules>

<available_skills>
`)
	for _, skill := range skills {
		fmt.Fprintf(&prompt, "  <skill>\n    <name>%s</name>\n    <description>%s</description>\n  </skill>\n", html.EscapeString(skill.Name), html.EscapeString(skill.Description))
	}
	prompt.WriteString("</available_skills>")
	return prompt.String()
}

func validateProjectConfig(config projectConfig, main agentDefinition) (string, string, providerConfig, time.Duration, error) {
	if err := validateLoggingConfig(config.Logging); err != nil {
		return "", "", providerConfig{}, 0, err
	}
	if len(config.Providers) == 0 {
		return "", "", providerConfig{}, 0, errors.New("providers must contain at least one provider")
	}
	providerNames := make([]string, 0, len(config.Providers))
	for providerName := range config.Providers {
		providerNames = append(providerNames, providerName)
	}
	sort.Strings(providerNames)
	for _, providerName := range providerNames {
		currentProvider := config.Providers[providerName]
		if strings.TrimSpace(providerName) == "" {
			return "", "", providerConfig{}, 0, errors.New("provider name is required")
		}
		if _, err := validateProviderConfig(providerName, currentProvider); err != nil {
			return "", "", providerConfig{}, 0, err
		}
	}
	if compaction := config.Compaction; compaction != nil && compaction.Auto {
		providerName := strings.TrimSpace(compaction.Provider)
		if providerName == "" {
			return "", "", providerConfig{}, 0, errors.New("compaction provider is required when compaction is enabled")
		}
		if strings.TrimSpace(compaction.Model) == "" {
			return "", "", providerConfig{}, 0, errors.New("compaction model is required when compaction is enabled")
		}
		compactionProvider, found := config.Providers[providerName]
		if !found {
			return "", "", providerConfig{}, 0, fmt.Errorf("compaction provider %q is not configured", providerName)
		}
		if _, err := validateProviderConfig(providerName, compactionProvider); err != nil {
			return "", "", providerConfig{}, 0, err
		}
	}
	providerName := strings.TrimSpace(main.Provider)
	selectedProvider, found := config.Providers[providerName]
	if !found {
		return "", "", providerConfig{}, 0, fmt.Errorf("main agent provider %q is not configured", providerName)
	}
	modelName := strings.TrimSpace(main.Model)
	timeout, err := validateProviderConfig(providerName, selectedProvider)
	if err != nil {
		return "", "", providerConfig{}, 0, err
	}
	if !permission.IsValidMode(config.PermissionMode) {
		return "", "", providerConfig{}, 0, fmt.Errorf("unknown permission_mode %q", config.PermissionMode)
	}
	return providerName, modelName, selectedProvider, timeout, nil
}

func expandProjectConfig(config *projectConfig) {
	for name, providerConfig := range config.Providers {
		providerConfig.Type = providerType(strings.ToLower(os.ExpandEnv(strings.TrimSpace(string(providerConfig.Type)))))
		providerConfig.URL = os.ExpandEnv(strings.TrimSpace(providerConfig.URL))
		providerConfig.APIKey = os.ExpandEnv(strings.TrimSpace(providerConfig.APIKey))
		providerConfig.RequestTimeout = os.ExpandEnv(strings.TrimSpace(providerConfig.RequestTimeout))
		for modelName, modelConfig := range providerConfig.Models {
			modelConfig.ExtraBody = expandEnvironmentJSONMap(modelConfig.ExtraBody)
			providerConfig.Models[modelName] = modelConfig
		}
		config.Providers[name] = providerConfig
	}
	if config.Compaction != nil {
		config.Compaction.Provider = os.ExpandEnv(strings.TrimSpace(config.Compaction.Provider))
		config.Compaction.Model = os.ExpandEnv(strings.TrimSpace(config.Compaction.Model))
	}
}

func expandEnvironmentJSONMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	expanded := make(map[string]any, len(values))
	for key, value := range values {
		expanded[key] = expandEnvironmentJSONValue(value)
	}
	return expanded
}

func expandEnvironmentJSONValue(value any) any {
	switch value := value.(type) {
	case string:
		return os.ExpandEnv(value)
	case map[string]any:
		return expandEnvironmentJSONMap(value)
	case []any:
		expanded := make([]any, len(value))
		for index, item := range value {
			expanded[index] = expandEnvironmentJSONValue(item)
		}
		return expanded
	default:
		return value
	}
}

func cloneCompactionConfig(config *compactionConfig) *compactionConfig {
	if config == nil {
		return nil
	}
	clone := *config
	return &clone
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func validateProviderConfig(providerName string, providerConfig providerConfig) (time.Duration, error) {
	if providerConfig.Type == "" {
		return 0, fmt.Errorf("provider %q type is required", providerName)
	}
	if providerConfig.Type != providerTypeOpenAI {
		return 0, unsupportedProviderType(providerName, providerConfig.Type)
	}
	if strings.TrimSpace(providerConfig.APIKey) == "" {
		return 0, fmt.Errorf("provider %q api_key is required", providerName)
	}
	for modelName, modelConfig := range providerConfig.Models {
		trimmedModelName := strings.TrimSpace(modelName)
		if trimmedModelName == "" {
			return 0, fmt.Errorf("provider %q model name is required", providerName)
		}
		if trimmedModelName != modelName {
			return 0, fmt.Errorf("provider %q model name %q must not have surrounding whitespace", providerName, modelName)
		}
		if _, err := json.Marshal(modelConfig.ExtraBody); err != nil {
			return 0, fmt.Errorf("provider %q model %q extra_body must contain valid JSON values: %w", providerName, modelName, err)
		}
		if _, _, err := configuredProviderMetadata(providerName, modelName, providerConfig); err != nil {
			return 0, err
		}
	}
	timeout := 2 * time.Minute
	if providerConfig.RequestTimeout != "" {
		parsed, err := time.ParseDuration(providerConfig.RequestTimeout)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("provider %q request_timeout must be a positive duration", providerName)
		}
		timeout = parsed
	}
	return timeout, nil
}

func validateLoggingConfig(config *loggingConfig) error {
	if config == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(config.Level)) {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return fmt.Errorf("logging level must be one of debug, info, warn, or error: %q", config.Level)
	}
}

func unsupportedProviderType(providerName string, providerType providerType) error {
	return fmt.Errorf("provider %q has unsupported type %q; supported types: %s", providerName, providerType, providerTypeOpenAI)
}

func selectProjectSkills(all map[string]skill, names []string) (map[string]skill, error) {
	selected := make(map[string]skill, len(names))
	for _, name := range names {
		skill, found := all[name]
		if !found {
			return nil, fmt.Errorf("skill %q is not available", name)
		}
		selected[name] = skill
	}
	return selected, nil
}

func loadSkills(root string) (map[string]skill, error) {
	skills := make(map[string]skill)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return skills, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skill directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		contents, err := readProjectFile(path, true)
		if err != nil {
			return nil, fmt.Errorf("load skill %q: %w", entry.Name(), err)
		}
		skill, err := parseSkill(path, contents)
		if err != nil {
			return nil, err
		}
		if skill.Name != entry.Name() {
			return nil, fmt.Errorf("skill %s: name %q must match directory %q", path, skill.Name, entry.Name())
		}
		if _, duplicate := skills[skill.Name]; duplicate {
			return nil, fmt.Errorf("duplicate skill name %q", skill.Name)
		}
		skills[skill.Name] = skill
	}
	return skills, nil
}

func parseSkill(path string, contents []byte) (skill, error) {
	text := strings.ReplaceAll(string(contents), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return skill{}, fmt.Errorf("skill %s: YAML front matter must start with ---", path)
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return skill{}, fmt.Errorf("skill %s: YAML front matter is not closed", path)
	}
	end += 4
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := decodeYAML([]byte(text[4:end]), &metadata); err != nil {
		return skill{}, fmt.Errorf("skill %s metadata: %w", path, err)
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	metadata.Description = strings.TrimSpace(metadata.Description)
	instructions := strings.TrimSpace(text[end+5:])
	if metadata.Name == "" || metadata.Description == "" {
		return skill{}, fmt.Errorf("skill %s: name and description are required", path)
	}
	if len(metadata.Name) > 64 || !skillNamePattern.MatchString(metadata.Name) {
		return skill{}, fmt.Errorf("skill %s: name must be at most 64 lowercase letters, numbers, or hyphen-separated words", path)
	}
	if strings.Contains(metadata.Name, "anthropic") || strings.Contains(metadata.Name, "claude") {
		return skill{}, fmt.Errorf("skill %s: name contains a reserved word", path)
	}
	if len(metadata.Description) > 1024 || strings.ContainsAny(metadata.Description, "<>") {
		return skill{}, fmt.Errorf("skill %s: description must be at most 1024 characters and cannot contain XML tags", path)
	}
	if instructions == "" {
		return skill{}, fmt.Errorf("skill %s: Markdown instructions are required", path)
	}
	return skill{Name: metadata.Name, Description: metadata.Description, Instructions: instructions, Path: path}, nil
}

func readProjectFile(path string, required bool) ([]byte, error) {
	linkInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symbolic links are not allowed")
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, errors.New("path must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxProjectFileSize {
		return nil, fmt.Errorf("file exceeds %d bytes", maxProjectFileSize)
	}
	buffer, err := io.ReadAll(io.LimitReader(file, maxProjectFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(buffer) > maxProjectFileSize {
		return nil, fmt.Errorf("file exceeds %d bytes", maxProjectFileSize)
	}
	return buffer, nil
}

func decodeYAML(contents []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("YAML must contain exactly one document")
		}
		return err
	}
	return nil
}
