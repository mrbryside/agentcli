package agentcli

import (
	"bytes"
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
	// SkillLoaderToolName is reserved for the framework's progressive skill
	// loader. Applications must not register a custom tool with this name.
	SkillLoaderToolName = toolexecution.SkillLoaderToolName
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ProjectConfig is loaded from .agentcli/config.yaml. Main-agent identity and
// capabilities live in .agentcli/MAIN.md.
type ProjectConfig struct {
	PermissionMode permission.Mode           `yaml:"permission_mode"`
	Providers      map[string]ProviderConfig `yaml:"providers"`
}

// ProviderType selects the protocol adapter used by a named connection
// profile. Map keys under providers are application-defined aliases.
type ProviderType string

const (
	// ProviderTypeOpenAI selects the OpenAI-compatible chat-completions adapter.
	ProviderTypeOpenAI ProviderType = "openai"
)

type ProviderConfig struct {
	Type           ProviderType `yaml:"type"`
	URL            string       `yaml:"url"`
	APIKey         string       `yaml:"api_key"`
	RequestTimeout string       `yaml:"request_timeout"`
}

// Skill is one .agentcli/skill/<name>/SKILL.md file. Only name and
// description are YAML metadata; Instructions is the Markdown body.
type Skill struct {
	Name         string
	Description  string
	Instructions string
	Path         string
}

// Project is an immutable snapshot of project instructions, skills, and
// provider configuration.
type Project struct {
	root          string
	agents        string
	main          AgentDefinition
	config        ProjectConfig
	skills        map[string]Skill // skills available to this root/child view
	allSkills     map[string]Skill // complete project catalog for child allowlists
	subagents     map[string]SubagentDefinition
	providerName  string
	modelName     string
	toolNames     []string
	restrictTools bool
	timeout       time.Duration
}

// LoadProject reads AGENTS.md, .agentcli/MAIN.md, .agentcli/config.yaml, and
// the configured skill and subagent definitions under root.
func LoadProject(root string) (*Project, error) {
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
	var config ProjectConfig
	if err := decodeYAML(configBytes, &config); err != nil {
		return nil, fmt.Errorf("decode %s: %w", configPath, err)
	}
	expandProjectConfig(&config)
	if config.PermissionMode == "" {
		config.PermissionMode = permission.Default
	}

	agentsBytes, err := readProjectFile(filepath.Join(absoluteRoot, "AGENTS.md"), false)
	if err != nil {
		return nil, fmt.Errorf("load AGENTS.md: %w", err)
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
	config.Providers[providerName] = providerConfig
	return &Project{
		root: absoluteRoot, agents: string(agentsBytes), main: mainDefinition, config: config,
		skills: rootSkills, allSkills: allSkills, subagents: subagents,
		providerName: providerName, modelName: modelName,
		toolNames: append([]string{}, mainDefinition.Tools...), restrictTools: true,
		timeout: timeout,
	}, nil
}

// WithProject applies a loaded project to Agent.New. It selects the configured
// model, project permission identity, permission mode, system prompts, and
// skill loader. A later Agent option may override scalar values.
func WithProject(project *Project) Option {
	return func(configuration *config) error {
		if project == nil {
			return errors.New("project is required")
		}
		model, err := project.Model()
		if err != nil {
			return err
		}
		configuration.model = model
		configuration.projectRoot = project.root
		configuration.systemPrompts = append(configuration.systemPrompts, project.SystemPrompts()...)
		configuration.project = project
		configuration.permissionMode = project.PermissionMode()
		configuration.permissionPolicy.Mode = project.PermissionMode()
		return nil
	}
}

// Model constructs the configured OpenAI-compatible model adapter.
func (project *Project) Model() (agentruntime.Model, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}
	return project.ModelFor(project.providerName, project.ModelName())
}

// ModelFor constructs a model for a named project provider profile and model.
// The profile's type selects the protocol adapter; URL, credential, and
// timeout always remain in project config.
func (project *Project) ModelFor(providerName, model string) (agentruntime.Model, error) {
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
	case ProviderTypeOpenAI:
		return openaiadapter.New(
			provideropenai.NewProvider(provideropenai.Config{
				URL: providerConfig.URL, APIKey: providerConfig.APIKey, Timeout: timeout,
			}),
			openaiadapter.Config{Model: model},
		), nil
	default:
		return nil, unsupportedProviderType(providerName, providerConfig.Type)
	}
}

func (project *Project) Root() string {
	if project == nil {
		return ""
	}
	return project.root
}

func (project *Project) ProviderName() string {
	if project == nil {
		return ""
	}
	return project.providerName
}

func (project *Project) ModelName() string {
	if project == nil {
		return ""
	}
	return project.modelName
}

// ToolNames returns the main agent's configured custom-tool allowlist.
func (project *Project) ToolNames() []string {
	if project == nil || !project.restrictTools {
		return nil
	}
	return append([]string{}, project.toolNames...)
}

func (project *Project) PermissionMode() permission.Mode {
	if project == nil {
		return ""
	}
	return project.config.PermissionMode
}

// Skills returns discovered skills in stable name order.
func (project *Project) Skills() []Skill {
	if project == nil {
		return nil
	}
	names := make([]string, 0, len(project.skills))
	for name := range project.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	skills := make([]Skill, len(names))
	for index, name := range names {
		skills[index] = project.skills[name]
	}
	return skills
}

// MainAgent returns the main-agent definition loaded from .agentcli/MAIN.md.
func (project *Project) MainAgent() AgentDefinition {
	if project == nil {
		return AgentDefinition{}
	}
	return cloneSubagentDefinition(project.main)
}

// Subagents returns discovered subagent definitions in stable name order.
func (project *Project) Subagents() []SubagentDefinition {
	if project == nil {
		return nil
	}
	return sortedSubagentDefinitions(project.subagents)
}

// SystemPrompts returns one grouped framework prompt and keeps AGENTS.md as a
// separate system message. Full skill bodies are loaded only through
// load_skill after the model selects a skill by description.
func (project *Project) SystemPrompts() []string {
	if project == nil {
		return nil
	}
	prompts := make([]string, 0, 2)
	if frameworkPrompt := project.mainAgentSystemPrompt(); frameworkPrompt != "" {
		prompts = append(prompts, frameworkPrompt)
	}
	if strings.TrimSpace(project.agents) != "" {
		prompts = append(prompts, project.agents)
	}
	return prompts
}

func (project *Project) subagentDiscoveryPrompt() string {
	var prompt strings.Builder
	prompt.WriteString(`You have access to optional configured subagents. You are the only agent allowed to create, message, inspect, or close them; children never receive subagent-management tools and cannot create nested agents or manage siblings.

The default is to answer the user directly. A subagent is an orchestration capability, not a mandatory step whenever a definition appears relevant. The available_subagents catalog contains every child's name, description, provider, model, allowed skills, and allowed custom tools. Questions that only ask what is available or which child might fit are discovery-only: answer directly from this catalog and do not start a child. Do not delegate simple answers, conversational follow-ups, explanations, translations, formatting, or other self-contained single-response tasks. Mere topic overlap is not sufficient. Start a subagent only when specialized independent investigation, substantial context isolation, parallel work, or explicit user-requested delegation materially helps. Never claim a child was started unless start_subagent succeeded.

Every child turn is asynchronous. start_subagent and send_subagent_message return immediately; completion, failure, or interruption is delivered later as a callback containing that turn's final answer or terminal error. Never poll while waiting. Do not repeatedly call list_subagents or subagent_status to check whether a callback has arrived. list_subagents is for explicit discovery. subagent_status is for an explicit user status question or an immediate operation that truly requires one snapshot; call it at most once and answer from that snapshot.

Every child instance has a short random display_name in active_subagents. Use that friendly name when discussing the child with the user and map it to the child id when calling tools. For follow-up intent naming a child, call send_subagent_message. If the user vaguely asks to continue and exactly one child is open, continue that child instead of creating another. If multiple children are open and the intended child is unclear, ask the user which display_name they mean; do not guess. Set start_subagent new_instance=true only for explicit new, another, separate, or parallel intent. The runtime also enforces these reuse and selection rules if start_subagent is called without new_instance.

When a callback arrives, choose independently whether to use its result now, perform useful work while other children continue, request a focused missing detail with send_subagent_message, or finish this parent turn and wait passively. Waiting means ending the current turn; another callback will automatically resume you. After send_subagent_message, do not poll: use its later callback. The active_subagents reminder is the current lifecycle snapshot and is sufficient to know which other children are still running. A callback result is authoritative for the completed child turn. Treat bounded one-shot work as finished after consuming and delivering its result: close that child unless there is a concrete planned follow-up, queued work, unresolved work requiring the same context, or explicit ongoing collaboration. The mere possibility that the user may ask something later is not a reason to keep it open.

<available_subagents>
`)
	for _, definition := range project.Subagents() {
		fmt.Fprintf(&prompt, "  <subagent>\n    <name>%s</name>\n    <description>%s</description>\n    <provider>%s</provider>\n    <model>%s</model>\n    <skills>", html.EscapeString(definition.Name), html.EscapeString(definition.Description), html.EscapeString(definition.Provider), html.EscapeString(definition.Model))
		for _, skillName := range definition.Skills {
			fmt.Fprintf(&prompt, "<skill>%s</skill>", html.EscapeString(skillName))
		}
		prompt.WriteString("</skills>\n    <tools>")
		for _, toolName := range definition.Tools {
			fmt.Fprintf(&prompt, "<tool>%s</tool>", html.EscapeString(toolName))
		}
		prompt.WriteString("</tools>\n  </subagent>\n")
	}
	prompt.WriteString("</available_subagents>")
	return prompt.String()
}

func (project *Project) skillDiscoveryPrompt() string {
	var prompt strings.Builder
	prompt.WriteString("You have access to optional skills. The available_skills catalog already contains every skill's name and description. Questions that only ask which skills are available, what they do, or which skill might fit are discovery-only: answer directly from this catalog and MUST NOT call load_skill. Call load_skill only when you are about to apply that skill's full instructions to perform the user's task, or when the user explicitly asks to inspect its full instructions. Once loaded, keep using instructions already present in recent conversation history; do not call load_skill again merely because a later request matches the same description. You may call it again when the prior instructions are old or no longer visible, and the runtime will decide whether a refresh is needed. If load_skill returns already_loaded, continue the task and MUST NOT call it again in the same turn. Never load an irrelevant skill as a substitute for a missing tool or capability; state the limitation instead. Do not claim to have applied a skill unless load_skill succeeded. If no skill is relevant, continue without loading one.\n\n<available_skills>\n")
	for _, skill := range project.Skills() {
		fmt.Fprintf(&prompt, "  <skill>\n    <name>%s</name>\n    <description>%s</description>\n  </skill>\n", html.EscapeString(skill.Name), html.EscapeString(skill.Description))
	}
	prompt.WriteString("</available_skills>")
	return prompt.String()
}

func validateProjectConfig(config ProjectConfig, main AgentDefinition) (string, string, ProviderConfig, time.Duration, error) {
	if len(config.Providers) == 0 {
		return "", "", ProviderConfig{}, 0, errors.New("providers must contain at least one provider")
	}
	providerNames := make([]string, 0, len(config.Providers))
	for providerName := range config.Providers {
		providerNames = append(providerNames, providerName)
	}
	sort.Strings(providerNames)
	for _, providerName := range providerNames {
		providerConfig := config.Providers[providerName]
		if strings.TrimSpace(providerName) == "" {
			return "", "", ProviderConfig{}, 0, errors.New("provider name is required")
		}
		if _, err := validateProviderConfig(providerName, providerConfig); err != nil {
			return "", "", ProviderConfig{}, 0, err
		}
	}
	providerName := strings.TrimSpace(main.Provider)
	providerConfig, found := config.Providers[providerName]
	if !found {
		return "", "", ProviderConfig{}, 0, fmt.Errorf("main agent provider %q is not configured", providerName)
	}
	modelName := strings.TrimSpace(main.Model)
	timeout, err := validateProviderConfig(providerName, providerConfig)
	if err != nil {
		return "", "", ProviderConfig{}, 0, err
	}
	if !permission.IsValidMode(config.PermissionMode) {
		return "", "", ProviderConfig{}, 0, fmt.Errorf("unknown permission_mode %q", config.PermissionMode)
	}
	return providerName, modelName, providerConfig, timeout, nil
}

func expandProjectConfig(config *ProjectConfig) {
	for name, providerConfig := range config.Providers {
		providerConfig.Type = ProviderType(strings.ToLower(os.ExpandEnv(strings.TrimSpace(string(providerConfig.Type)))))
		providerConfig.URL = os.ExpandEnv(strings.TrimSpace(providerConfig.URL))
		providerConfig.APIKey = os.ExpandEnv(strings.TrimSpace(providerConfig.APIKey))
		providerConfig.RequestTimeout = os.ExpandEnv(strings.TrimSpace(providerConfig.RequestTimeout))
		config.Providers[name] = providerConfig
	}
}

func validateProviderConfig(providerName string, providerConfig ProviderConfig) (time.Duration, error) {
	if providerConfig.Type == "" {
		return 0, fmt.Errorf("provider %q type is required", providerName)
	}
	if providerConfig.Type != ProviderTypeOpenAI {
		return 0, unsupportedProviderType(providerName, providerConfig.Type)
	}
	if strings.TrimSpace(providerConfig.APIKey) == "" {
		return 0, fmt.Errorf("provider %q api_key is required", providerName)
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

func unsupportedProviderType(providerName string, providerType ProviderType) error {
	return fmt.Errorf("provider %q has unsupported type %q; supported types: %s", providerName, providerType, ProviderTypeOpenAI)
}

func selectProjectSkills(all map[string]Skill, names []string) (map[string]Skill, error) {
	selected := make(map[string]Skill, len(names))
	for _, name := range names {
		skill, found := all[name]
		if !found {
			return nil, fmt.Errorf("skill %q is not available", name)
		}
		selected[name] = skill
	}
	return selected, nil
}

func loadSkills(root string) (map[string]Skill, error) {
	skills := make(map[string]Skill)
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

func parseSkill(path string, contents []byte) (Skill, error) {
	text := strings.ReplaceAll(string(contents), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Skill{}, fmt.Errorf("skill %s: YAML front matter must start with ---", path)
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return Skill{}, fmt.Errorf("skill %s: YAML front matter is not closed", path)
	}
	end += 4
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := decodeYAML([]byte(text[4:end]), &metadata); err != nil {
		return Skill{}, fmt.Errorf("skill %s metadata: %w", path, err)
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	metadata.Description = strings.TrimSpace(metadata.Description)
	instructions := strings.TrimSpace(text[end+5:])
	if metadata.Name == "" || metadata.Description == "" {
		return Skill{}, fmt.Errorf("skill %s: name and description are required", path)
	}
	if len(metadata.Name) > 64 || !skillNamePattern.MatchString(metadata.Name) {
		return Skill{}, fmt.Errorf("skill %s: name must be at most 64 lowercase letters, numbers, or hyphen-separated words", path)
	}
	if strings.Contains(metadata.Name, "anthropic") || strings.Contains(metadata.Name, "claude") {
		return Skill{}, fmt.Errorf("skill %s: name contains a reserved word", path)
	}
	if len(metadata.Description) > 1024 || strings.ContainsAny(metadata.Description, "<>") {
		return Skill{}, fmt.Errorf("skill %s: description must be at most 1024 characters and cannot contain XML tags", path)
	}
	if instructions == "" {
		return Skill{}, fmt.Errorf("skill %s: Markdown instructions are required", path)
	}
	return Skill{Name: metadata.Name, Description: metadata.Description, Instructions: instructions, Path: path}, nil
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
