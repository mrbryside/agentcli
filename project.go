package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
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
	PermissionMode   permission.Mode           `yaml:"permission_mode"`
	MaxProviderSteps int                       `yaml:"max_provider_steps"`
	MaxSubagents     int                       `yaml:"max_subagents"`
	Providers        map[string]ProviderConfig `yaml:"providers"`
	Compaction       *CompactionConfig         `yaml:"compaction"`
	Logging          *LoggingConfig            `yaml:"logging"`
	Observability    *ObservabilityConfig      `yaml:"observability"`
}

// LoggingConfig controls structured runtime lifecycle logs written to stderr.
// Omitting the section disables runtime logging. When the section is present,
// enabled defaults to true and level defaults to info.
type LoggingConfig struct {
	Enabled bool   `yaml:"enabled"`
	Level   string `yaml:"level"`
}

// UnmarshalYAML applies useful opt-in defaults while keeping the section
// strict even though custom YAML unmarshalling bypasses Decoder.KnownFields.
func (config *LoggingConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return errors.New("logging must be a mapping")
	}
	for index := 0; index < len(value.Content); index += 2 {
		key := value.Content[index].Value
		if key != "enabled" && key != "level" {
			return fmt.Errorf("field %s not found in type agentcli.LoggingConfig", key)
		}
	}
	type rawLoggingConfig LoggingConfig
	decoded := rawLoggingConfig{Enabled: true, Level: "info"}
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*config = LoggingConfig(decoded)
	return nil
}

// ObservabilityConfig contains optional project-wide telemetry backends.
// A nil backend leaves model calls uninstrumented.
type ObservabilityConfig struct {
	Langfuse *LangfuseConfig `yaml:"langfuse"`
}

// LangfuseConfig sends LLM-call spans to Langfuse through its OTLP/HTTP
// endpoint. Credentials may contain ${VARIABLE} references and are expanded
// when the project is loaded.
type LangfuseConfig struct {
	Enabled     bool                  `yaml:"enabled"`
	BaseURL     string                `yaml:"base_url"`
	PublicKey   string                `yaml:"public_key"`
	SecretKey   string                `yaml:"secret_key"`
	Environment string                `yaml:"environment"`
	ServiceName string                `yaml:"service_name"`
	Release     string                `yaml:"release"`
	SampleRate  *float64              `yaml:"sample_rate"`
	Capture     LangfuseCaptureConfig `yaml:"capture"`
}

// LangfuseCaptureConfig controls potentially sensitive generation payloads.
// All fields default to false.
type LangfuseCaptureConfig struct {
	Input     bool `yaml:"input"`
	Output    bool `yaml:"output"`
	Reasoning bool `yaml:"reasoning"`
}

// CompactionConfig selects the project provider profile and model used to
// compact an agent transcript. Model limits belong to ProviderConfig so every
// main, child, and compaction model uses its own provider profile's metadata.
// A nil value means compaction is disabled; when the section is present Auto
// defaults to true.
type CompactionConfig struct {
	Auto     bool   `yaml:"auto"`
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// UnmarshalYAML makes an explicitly configured compaction section opt in by
// default while preserving auto: false as an explicit disable. Custom YAML
// unmarshalling bypasses Decoder.KnownFields, so keep this section strict.
func (config *CompactionConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return errors.New("compaction must be a mapping")
	}
	for index := 0; index < len(value.Content); index += 2 {
		key := value.Content[index].Value
		if key != "auto" && key != "provider" && key != "model" {
			return fmt.Errorf("field %s not found in type agentcli.CompactionConfig", key)
		}
	}
	type rawCompactionConfig CompactionConfig
	decoded := rawCompactionConfig{Auto: true}
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*config = CompactionConfig(decoded)
	return nil
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
	// Models contains optional exact-name overrides. It does not restrict which
	// model names agents may select.
	Models map[string]ProviderModelConfig `yaml:"models"`
}

// ProviderModelConfig contains capability and request overrides for one exact
// model name within a provider profile.
type ProviderModelConfig struct {
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
	main          AgentDefinition
	config        ProjectConfig
	skills        map[string]Skill // skills available to this root/child view
	allSkills     map[string]Skill // complete project catalog for child allowlists
	subagents     map[string]SubagentDefinition
	providerName  string
	modelName     string
	compaction    *CompactionConfig
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
	var config ProjectConfig
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
		configuration.logger = projectLogger(project.config.Logging)
		if project.MaxProviderSteps() > 0 {
			configuration.maxProviderSteps = project.MaxProviderSteps()
		}
		if project.compaction != nil && project.compaction.Auto {
			compactionModel, err := project.CompactionModel()
			if err != nil {
				return fmt.Errorf("resolve compaction model: %w", err)
			}
			configuration.compactionModel = compactionModel
		}
		if project.MaxSubagents() > 0 {
			configuration.maxSubagents = project.MaxSubagents()
		}
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

// Compaction returns the configured transcript-compaction policy. Its boolean
// result reports whether the compaction section is present. The returned value
// is safe for callers to modify.
func (project *Project) Compaction() (CompactionConfig, bool) {
	if project == nil || project.compaction == nil {
		return CompactionConfig{}, false
	}
	return *cloneCompactionConfig(project.compaction), true
}

// CompactionModel constructs the configured compaction model through the same
// provider-profile factory used for agent models.
func (project *Project) CompactionModel() (agentruntime.Model, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}
	if project.compaction == nil || !project.compaction.Auto {
		return nil, errors.New("compaction is disabled")
	}
	return project.ModelFor(project.compaction.Provider, project.compaction.Model)
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

// MaxProviderSteps returns the configured maximum number of provider rounds
// per turn. Zero means the runtime default is used.
func (project *Project) MaxProviderSteps() int {
	if project == nil {
		return 0
	}
	return project.config.MaxProviderSteps
}

// MaxSubagents returns the configured maximum number of non-closed child
// instances per parent session. Zero means the Agent default is used.
func (project *Project) MaxSubagents() int {
	if project == nil {
		return 0
	}
	return project.config.MaxSubagents
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

// SystemPrompts returns the framework prompt, optional focused skill and
// subagent prompts, and MAIN.md instructions as separate ordered system
// messages. Full skill bodies are loaded only through load_skill after the
// model selects a skill by description or follows an explicit load requirement.
func (project *Project) SystemPrompts() []string {
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
				"## IMPORTANT: Tool-result protocol\n\n"+mainAgentSubagentToolPrompt+
				"\n\n## Orchestration rules and available subagents\n\n"+project.subagentDiscoveryPrompt(),
		)
	}
	if instructions := strings.TrimSpace(project.main.Instructions); instructions != "" {
		prompts = append(prompts, "# Main agent instructions\n\n"+instructions)
	}
	return prompts
}

func (project *Project) subagentDiscoveryPrompt() string {
	var prompt strings.Builder
	prompt.WriteString(`You have access to configured subagents whose types and capabilities are listed in available_subagents.

<subagent_orchestration_rules>
## Catalog and authority

available_subagents is the complete catalog of configured agent types. Each description is selection metadata: it explains the focused work that type can perform. You are the only agent allowed to create or message children. Children never receive subagent-management tools, cannot create nested agents, and cannot manage siblings. Destructive child closure is application-owned and is not available as a model tool.

The runtime supplies live instance summaries through active_subagents; there are no model-facing list or status tools. available_subagents names types, while active_subagents names instances with a random display_name and stable id.

## Trigger precedence and selection

A subagent may be triggered in either of these ways:

1. Explicit requirement: another applicable instruction or the user explicitly requires delegation or a particular subagent. This trigger takes precedence when that configured type and its required capabilities are available.
2. Description match: when no applicable instruction already requires a different delegation route, a subagent description directly matches a focused delegated task and delegation materially helps through specialized independent work, substantial context isolation, or useful parallelism.

Explicit requirements take precedence over description matching. Before selecting a subagent by description, check all applicable instructions and the user's request for required delegation or a required agent type. If one exists, use that required route first. Never replace, bypass, or delay it because another subagent description appears to match the task more closely. When explicit requirements conflict, follow normal instruction priority rather than description similarity. After satisfying the explicit requirement, start another subagent only if a separate valid trigger still applies.

Absent an explicit requirement, the default is to answer directly. Mere topic overlap is not a direct description match, and normal conversation, simple answers, explanations, translations, formatting, or other self-contained work do not trigger delegation by themselves. An applicable explicit requirement remains a valid trigger when the configured type and required capabilities are available. Questions asking which agents are available or what they do are discovery-only: answer from available_subagents and do not start a child unless another applicable instruction explicitly requires delegation.

After a valid trigger, select the single best matching definition and call start_subagent with its exact name, a self-contained focused assignment, and the required continue_after_dispatch choice described in the important tool-result protocol above. A description helps select the type but is not the child's full instructions and does not prove that any work has started.

## Instance selection and fan-out

Speak to the user using display_name; pass the stable id to instance tools. Before every start_subagent call, decide whether the assignment belongs in an existing child's conversation or a genuinely new child. new_instance=false creates a child when none is open, but explicitly permits reuse of the sole open child of the requested definition; use it only when that possible reuse is acceptable. Children of a different definition are never implicit reuse candidates. If several matching children are open and intent is ambiguous, ask which display_name the user means.

Set new_instance=true for a genuinely new assignment that must run in a separate child, even when another child of the same definition is open. Examples include an intentional independent comparison or parallel task. It is not a retry mechanism and must never bypass pending work. To continue a specific idle child after its latest callback was received and consumed, call send_subagent_message with that child's stable id instead of using start_subagent as an indirect follow-up. Prefer one child at a time. Start multiple children in one provider response only for genuinely independent work whose comparison or parallel execution materially helps; ordinary lookup or research must start one child, assess its callback, and start another only if the evidence is still insufficient. Every start_subagent call in the same tool batch must use the same continue_after_dispatch value: false on every call when the parent should wait after the batch, or true on every call when already-planned independent parent work must continue after the batch. Never mix values. new_instance=true is not a retry mechanism.

## Dispatch result handling

Dispatch is not completion. start_subagent and send_subagent_message are always asynchronous. start_subagent ends a successful pending-callback tool batch when continue_after_dispatch=false and returns control to the parent model when true. send_subagent_message returns control to the parent model. Returning control does not require assistant content or another tool call. Their successful outer tool status means only that the invocation was handled. Count a child as dispatched only when the result has accepted=true. duplicate, already_sent, callback_pending, and selection_required return accepted=false.

After accepted=true, the authoritative callback may be injected at a safe provider boundary of the active parent; otherwise it arrives in a callback continuation turn. For start_subagent, follow the selected continue_after_dispatch behavior. When it is true, continue only the already-planned work that justified that choice. For send_subagent_message, follow the post-dispatch turn policy above. Never use start_subagent or send_subagent_message to check status, chase, remind, or request progress from running work. Wait for the automatic callback before interacting with that child again. This does not prohibit already-planned work outside the delegated task that is independent of the pending callback. Never redo delegated work, retry the same dispatch with changed wording, poll lifecycle state, narrate waiting, call a response or delivery tool while waiting, or claim completion before the callback.

selection_required has callback_action=none because no dispatch occurred; ask which display_name the user means. duplicate, already_sent, and callback_pending have callback_action=automatic_existing; they create no new callback, so never count or retry them and let the existing result arrive automatically.

## Callback handling

Callbacks are authoritative. Each child turn later produces completed, incomplete, or failed. completed means the child explicitly confirmed all delegated work is resolved. incomplete means required work, information, confirmation, or a decision remains. failed contains a terminal error. Use the callback's final answer, summary, and next_step directly; never infer outcome from a dispatch result or stale active_subagents data.

Read callback_progress before deciding what to do. pending_callbacks identifies accepted subagent dispatches whose callbacks are still outstanding. Never duplicate, replace, retry, poll, or report work already assigned to a pending subagent callback merely because it has not arrived. received_callbacks identifies the callback turns already delivered in the current response scope; process each exactly once.

When pending callbacks remain, continue only work that was already planned, is independent of every pending callback, does not duplicate or invalidate any delegated assignment, and will not need to be redone after those callbacks arrive. If no such work remains, make no more tool calls or assistant content; the runtime will resume the response when another callback arrives. Do not call a response or delivery tool while a required callback remains outstanding.

When no pending callbacks remain, combine the received outcomes once, then finish, continue, request clarification, or report a limitation according to the complete set of results. A newly accepted dispatch or follow-up creates another pending callback and reopens this callback barrier.

## Follow-up and lifecycle

send_subagent_message targets an idle child only, after its latest callback has been received and consumed. Never send to a running child and never use either subagent tool to follow up, check status, or remind it while its callback is pending. You may still perform already-planned work that is outside the delegated task and independent of that callback. After incomplete, ask the user for required information or send one focused follow-up; incomplete children remain open across response scopes. After completed, deliver the result. After failed, report the error and send a focused recovery instruction only when concrete recovery work is required.

Once a response scope is fully quiescent, the runtime automatically closes completed and failed children that are not referenced by another live scope. A later one-shot system reminder reports which children were automatically closed.

## Safety

Never claim a tool action occurred unless its complete result confirms it. Never reveal secrets returned by a child; redact them and warn the user.
</subagent_orchestration_rules>

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
	prompt.WriteString(`You have access to skills whose names and descriptions are listed in available_skills. Full instructions load progressively through load_skill.

<skill_rules>
## Catalog and selection

available_skills is the complete skill catalog available to this agent. Each description is selection metadata: use it to decide whether the skill directly matches the task. A description is not the skill's full instructions and never substitutes for loading a skill that must be applied.

## Load triggers and precedence

Load a skill for any of these reasons:

1. Explicit requirement: another applicable instruction explicitly requires that skill or requires a skill for the selected workflow. This trigger is mandatory; call load_skill before the action or answer it governs.
2. Description match: when no applicable instruction already requires a different skill for the governed workflow, you select a skill whose description directly matches the task and are about to apply that skill.
3. Explicit inspection: the user asks to inspect or read the skill's full instructions.

Explicit requirements take precedence over description matching. Before selecting a skill by description, check all applicable instructions for a required skill or workflow. If one exists, load and follow that required skill first. Never replace, bypass, or delay it because another skill description appears to match the task more closely. After satisfying the explicit requirement, load another skill only if a separate valid trigger still applies.

Tool descriptions, subagent descriptions, and other capability metadata may help choose a route, but never authorize bypassing a required skill load. If no skill directly matches and no applicable instruction requires one, continue without loading one.

## Non-triggers

Questions that only ask which skills are available, what they do, or which skill might fit are discovery-only. Answer directly from available_skills and MUST NOT call load_skill unless another applicable instruction explicitly requires it. Never load an irrelevant skill as a substitute for a missing tool or capability; state the limitation instead.

## Result handling

Inspect the complete load_skill result before continuing. Do not claim to have loaded or applied a skill unless the result confirms it. After a successful load, follow the returned instructions for the governed work.

Whenever a load trigger applies, always call load_skill before the action or answer it governs. Do not skip the call because the skill appears to have been loaded earlier or its instructions are visible in conversation history. Skill caching and freshness are runtime-managed.

If load_skill returns loaded or already_loaded, the load requirement is satisfied. Continue the task and do not call it repeatedly for the same trigger in the same turn.
</skill_rules>

<available_skills>
`)
	for _, skill := range project.Skills() {
		fmt.Fprintf(&prompt, "  <skill>\n    <name>%s</name>\n    <description>%s</description>\n  </skill>\n", html.EscapeString(skill.Name), html.EscapeString(skill.Description))
	}
	prompt.WriteString("</available_skills>")
	return prompt.String()
}

func validateProjectConfig(config ProjectConfig, main AgentDefinition) (string, string, ProviderConfig, time.Duration, error) {
	if config.MaxProviderSteps < 0 {
		return "", "", ProviderConfig{}, 0, errors.New("max_provider_steps cannot be negative")
	}
	if config.MaxSubagents < 0 {
		return "", "", ProviderConfig{}, 0, errors.New("max_subagents cannot be negative")
	}
	if err := validateLoggingConfig(config.Logging); err != nil {
		return "", "", ProviderConfig{}, 0, err
	}
	if err := validateObservabilityConfig(config.Observability); err != nil {
		return "", "", ProviderConfig{}, 0, err
	}
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
	if compaction := config.Compaction; compaction != nil && compaction.Auto {
		providerName := strings.TrimSpace(compaction.Provider)
		if providerName == "" {
			return "", "", ProviderConfig{}, 0, errors.New("compaction provider is required when compaction is enabled")
		}
		if strings.TrimSpace(compaction.Model) == "" {
			return "", "", ProviderConfig{}, 0, errors.New("compaction model is required when compaction is enabled")
		}
		providerConfig, found := config.Providers[providerName]
		if !found {
			return "", "", ProviderConfig{}, 0, fmt.Errorf("compaction provider %q is not configured", providerName)
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
	if config.Observability != nil && config.Observability.Langfuse != nil {
		langfuse := config.Observability.Langfuse
		langfuse.BaseURL = os.ExpandEnv(strings.TrimSpace(langfuse.BaseURL))
		langfuse.PublicKey = os.ExpandEnv(strings.TrimSpace(langfuse.PublicKey))
		langfuse.SecretKey = os.ExpandEnv(strings.TrimSpace(langfuse.SecretKey))
		langfuse.Environment = os.ExpandEnv(strings.TrimSpace(langfuse.Environment))
		langfuse.ServiceName = os.ExpandEnv(strings.TrimSpace(langfuse.ServiceName))
		langfuse.Release = os.ExpandEnv(strings.TrimSpace(langfuse.Release))
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

func cloneCompactionConfig(config *CompactionConfig) *CompactionConfig {
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

func validateObservabilityConfig(config *ObservabilityConfig) error {
	if config == nil || config.Langfuse == nil || !config.Langfuse.Enabled {
		return nil
	}
	langfuse := config.Langfuse
	if langfuse.PublicKey == "" {
		return errors.New("observability langfuse public_key is required when enabled")
	}
	if langfuse.SecretKey == "" {
		return errors.New("observability langfuse secret_key is required when enabled")
	}
	if langfuse.BaseURL != "" {
		parsed, err := url.Parse(langfuse.BaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("observability langfuse base_url must be an absolute HTTP(S) URL without query or fragment")
		}
	}
	if langfuse.SampleRate != nil && (*langfuse.SampleRate < 0 || *langfuse.SampleRate > 1) {
		return errors.New("observability langfuse sample_rate must be between 0 and 1")
	}
	if environment := langfuse.Environment; environment != "" {
		if len(environment) > 40 || strings.HasPrefix(environment, "langfuse") || !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(environment) {
			return errors.New("observability langfuse environment must be at most 40 lowercase letters, numbers, hyphens, or underscores and must not start with langfuse")
		}
	}
	return nil
}

func validateLoggingConfig(config *LoggingConfig) error {
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
