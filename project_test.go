package agentcli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestLoadProjectSeparatesMainInstructionsFromFrameworkPromptAndLoadsSkillsProgressively(t *testing.T) {
	root := projectFixture(t)
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if project.Root() != root || project.ProviderName() != "openai" || project.ModelName() != "gpt-test" || project.PermissionMode() != permission.CriticalOnly {
		t.Fatalf("project = root %q provider %q model %q mode %q", project.Root(), project.ProviderName(), project.ModelName(), project.PermissionMode())
	}
	main := project.MainAgent()
	if main.Name != "main" || main.Description != "" || main.Provider != "openai" || main.Model != "gpt-test" || !slices.Equal(main.Tools, []string{}) || !strings.Contains(main.Instructions, "Coordinate work") {
		t.Fatalf("main definition = %#v", main)
	}
	main.Skills[0] = "mutated"
	if project.MainAgent().Skills[0] == "mutated" {
		t.Fatal("main definition was not defensively copied")
	}
	skills := project.Skills()
	if names := []string{skills[0].Name, skills[1].Name}; !slices.Equal(names, []string{"reviewing-go", "testing-go"}) {
		t.Fatalf("skill names = %v", names)
	}
	prompts := project.SystemPrompts()
	if len(prompts) != 3 {
		t.Fatalf("system prompts = %#v", prompts)
	}
	if !strings.Contains(prompts[0], "# Runtime context") || !strings.Contains(prompts[0], `agent: "main"`) || !strings.Contains(prompts[0], `provider: "openai"`) || !strings.Contains(prompts[0], `model: "gpt-test"`) || !strings.Contains(prompts[0], `working_directory: "`+root+`"`) {
		t.Fatalf("main runtime context = %q", prompts[0])
	}
	if strings.Contains(prompts[0], "# Main agent instructions") ||
		strings.Contains(prompts[0], "# Skills") ||
		strings.Contains(prompts[0], "<available_skills>") ||
		strings.Contains(prompts[0], "Coordinate work and communicate the outcome clearly.") {
		t.Fatalf("framework prompt contains MAIN.md instructions: %q", prompts[0])
	}
	if !strings.Contains(prompts[1], "# Skills") ||
		!strings.Contains(prompts[1], "<available_skills>") ||
		!strings.Contains(prompts[1], "<name>testing-go</name>") ||
		!strings.Contains(prompts[1], "when Go tests are requested") {
		t.Fatalf("skill discovery prompt = %q", prompts[1])
	}
	if prompts[2] != "# Main agent instructions\n\nCoordinate work and communicate the outcome clearly." {
		t.Fatalf("MAIN.md system prompt = %q", prompts[2])
	}
	if !strings.Contains(prompts[0], "# Sensitive information") || !strings.Contains(prompts[0], modelSecretSafetyPrompt) {
		t.Fatalf("main secret-safety prompt = %q", prompts[0])
	}
	if !strings.Contains(prompts[0], "# Tool-result discipline") ||
		!strings.Contains(prompts[0], "Read the complete result") ||
		!strings.Contains(prompts[0], "task_id") ||
		!strings.Contains(prompts[0], "state") ||
		!strings.Contains(prompts[0], "Never claim more than the complete result confirms") {
		t.Fatalf("main tool-result discipline prompt = %q", prompts[0])
	}
	for _, expected := range []string{
		"tools listed with the current request are available now",
		"authoritative tool list",
		"do not claim it is missing or ask the user to enable it",
	} {
		if !strings.Contains(prompts[0], expected) {
			t.Fatalf("main tool-availability prompt missing %q: %q", expected, prompts[0])
		}
	}
	if !strings.Contains(prompts[0], "Give the user a clear, self-contained answer") ||
		strings.Contains(prompts[0], "required subagent results are still pending") {
		t.Fatalf("main response prompt = %q", prompts[0])
	}
	if !strings.Contains(prompts[1], "Do not load a skill merely to list or describe available skills") {
		t.Fatalf("skill discovery prompt does not prevent listing from loading a skill: %q", prompts[1])
	}
	for _, staleRule := range []string{
		"keep using instructions already present in recent conversation history",
		"do not call load_skill again merely because a later request matches",
	} {
		if strings.Contains(prompts[1], staleRule) {
			t.Fatalf("skill discovery prompt still lets the model infer cached state from history: %q", prompts[1])
		}
	}
	for _, expected := range []string{
		"<skill_rules>",
		"## Choose",
		"applicable instruction requires it",
		"description directly",
		"user asks to read its full",
		"Do not load a skill merely to list",
		"only valid load_skill names are exact <name> values",
		"Never copy a skill name from conversation history",
		"Explicit requirements take precedence over description",
		"## Load once per turn",
		"<turn_start> message marks a new turn",
		"Each skill may be loaded once",
		"status=loaded and name=<skill>",
		"Do not load it again until a new <turn_start>",
		"A different\nskill is separate",
		"## Read the result",
		"One load_skill call loads only the skill named",
		"instructions_in_context=true",
		"tools_unchanged=true",
		"already available in the",
		"tools listed with the current request are available now",
		"do not claim it is\n  missing",
		"does not require an\n  action",
		"Loading a skill only makes its instructions available",
		"Examples using only names from the current <available_skills>",
		"status=loaded, name=reviewing-go",
		"If reviewing-go is loaded and testing-go later has a separate valid trigger",
		"name=testing-go",
		"</skill_rules>",
	} {
		if !strings.Contains(prompts[1], expected) {
			t.Fatalf("skill discovery prompt does not contain structured rule %q: %q", expected, prompts[1])
		}
	}
	if strings.Contains(prompts[1], "Continue the task") {
		t.Fatalf("skill discovery prompt must not force post-load behavior: %q", prompts[1])
	}
	if strings.Contains(prompts[1], "Run go test ./...") {
		t.Fatalf("skill body was eagerly loaded in discovery prompt: %q", prompts[1])
	}
	if strings.Contains(strings.Join(prompts, "\n"), "Always explain failures clearly.") {
		t.Fatalf("AGENTS.md was included in system prompts: %#v", prompts)
	}

	configuration := defaultConfig(root)
	if err := WithProject(project)(&configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.systemPrompts) != 3 || len(configuration.tools) != 0 || configuration.project != project {
		t.Fatalf("applied project = prompts %d tools %#v project %p", len(configuration.systemPrompts), configuration.tools, configuration.project)
	}
	tool := newSkillLoader(project, configuration.messages, configuration.skillReload).tool()
	for _, expected := range []string{
		"exactly one skill",
		"current available_skills",
		"Use only an exact name listed there",
		"never copy an unlisted name from conversation history",
		"If no listed skill applies, do not call load_skill",
		"applicable instruction requires",
		"description directly matches",
		"user asks to read it",
		"once per <turn_start>",
		"status=loaded",
		"instructions_in_context=true",
		"already in the conversation",
		"does not load any other skill",
		"changes only instruction context",
		"tools listed with the current request are available now",
		"do not claim it is missing",
	} {
		if !strings.Contains(tool.Definition.Description, expected) {
			t.Fatalf("load_skill description does not contain trigger/result rule %q: %q", expected, tool.Definition.Description)
		}
	}
	if strings.Contains(tool.Definition.Description, "Continue the task") {
		t.Fatalf("load_skill description must not force post-load behavior: %q", tool.Definition.Description)
	}
	schemaJSON, err := json.Marshal(tool.Definition.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	schema := string(schemaJSON)
	if !strings.Contains(schema, "Exact skill name from available_skills") ||
		!strings.Contains(schema, "turn_start") {
		t.Fatalf("load_skill input schema does not repeat duplicate guard: %s", schema)
	}
	toolContext := toolexecution.WithInvocation(context.Background(), toolexecution.Invocation{
		SessionID: "session", TurnID: "turn", CallID: "call", ToolName: SkillLoaderToolName,
	})
	result, err := tool.Handler(toolContext, json.RawMessage(`{"name":"testing-go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "Run go test ./...") {
		t.Fatalf("loaded skill = %s", result)
	}
	if _, err := tool.Handler(toolContext, json.RawMessage(`{"name":"missing"}`)); err == nil {
		t.Fatal("missing skill unexpectedly loaded")
	} else if !strings.Contains(err.Error(), "current <available_skills>") ||
		!strings.Contains(err.Error(), "do not retry an unlisted name") {
		t.Fatalf("missing skill error does not give catalog-safe recovery: %v", err)
	}
}

func TestSkillDiscoveryExamplesComeFromCurrentCatalog(t *testing.T) {
	project, err := LoadProject(projectFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	prompt := project.skillDiscoveryPrompt()
	for _, applicationSpecificName := range []string{"web-research", "discord-live-server"} {
		if strings.Contains(prompt, applicationSpecificName) {
			t.Fatalf("framework skill prompt contains application-specific name %q: %q", applicationSpecificName, prompt)
		}
	}
	for _, expected := range []string{
		"status=loaded, name=reviewing-go",
		"status=loaded, name=reviewing-go, instructions_in_context=true",
		"If reviewing-go is loaded and testing-go later has a separate valid trigger",
		"name=testing-go",
		"<name>reviewing-go</name>",
		"<name>testing-go</name>",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("catalog-derived skill example missing %q: %q", expected, prompt)
		}
	}

	singleSkillPrompt := project.withSkills([]string{"testing-go"}).skillDiscoveryPrompt()
	if !strings.Contains(singleSkillPrompt, "status=loaded, name=testing-go") {
		t.Fatalf("single-skill prompt does not use its catalog name: %q", singleSkillPrompt)
	}
	if strings.Contains(singleSkillPrompt, "name=reviewing-go") ||
		strings.Contains(singleSkillPrompt, "later has a separate valid trigger") {
		t.Fatalf("single-skill prompt invented a second available skill: %q", singleSkillPrompt)
	}
}

func TestLoadProjectDoesNotReadRootAgentsMarkdown(t *testing.T) {
	root := projectFixture(t)
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.Remove(agentsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(agentsPath, 0o700); err != nil {
		t.Fatal(err)
	}

	project, err := LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject read AGENTS.md: %v", err)
	}
	if got := len(project.SystemPrompts()); got != 3 {
		t.Fatalf("system prompts = %d, want framework, skills, and MAIN.md instructions", got)
	}
}

func TestProjectTaskPromptSelectsForegroundParallelAndResumeWork(t *testing.T) {
	root := projectFixture(t)
	writeSubagentDefinition(t, root, "researcher", `---
name: researcher
description: Research current information from reliable sources.
provider: openai
model: gpt-test
---
Return evidence and limits.
`)
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.Join(project.SystemPrompts(), "\n")
	for _, expected := range []string{
		"Use task for focused work",
		"Application instructions may name a configured agent",
		"parallel,\nsequential, or continuing work in domain language",
		"Translate those instructions into the task calls",
		"Foreground is the default",
		"same tool batch",
		"run in parallel",
		"task_id",
		"presence of task_id always means resume",
		"preserve the same task_id",
		"Never remove task_id",
		"running, completed, incomplete, or error",
		"task_not_found, task_closed, or task_running",
		"<available_task_agents>",
		"<name>researcher</name>",
		"<description>Research current information from reliable sources.</description>",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("task prompt missing %q: %q", expected, prompt)
		}
	}
	for _, forbidden := range []string{
		"callback", "continue_main_agent", "accepted", "result_progress",
		"report_subagent_result", "send_subagent_message", "polling",
		"simulated waiting", "after the user answers, resume",
		"exactly two independent readers",
	} {
		if strings.Contains(strings.ToLower(prompt), forbidden) {
			t.Fatalf("task prompt contains retired protocol term %q: %q", forbidden, prompt)
		}
	}
}

func TestLoadProjectRejectsSubagentListingMainOnlyTaskTool(t *testing.T) {
	root := projectFixture(t)
	writeSubagentDefinition(t, root, "researcher", `---
name: researcher
description: Research current information.
provider: openai
model: gpt-test
tools: [task]
---
Return evidence.
`)
	if _, err := LoadProject(root); err == nil ||
		!strings.Contains(err.Error(), `subagent "researcher" cannot list main-agent task tool "task"`) {
		t.Fatalf("LoadProject() error = %v", err)
	}
}

func TestProjectRejectsSubagentListingEndResponseScopeTool(t *testing.T) {
	root := projectFixture(t)
	writeSubagentDefinition(t, root, "researcher", `---
name: researcher
description: Research current information.
provider: openai
model: gpt-test
tools: [deliver]
---
Return evidence.
`)
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(
		context.Background(),
		WithProject(project),
		WithModel(&scriptedModel{}),
		WithTool(toolexecution.Tool{
			Definition: agentruntime.ToolDefinition{Name: "deliver", InputSchema: agentruntime.ToolSchema{Type: "object"}},
			Handler:    func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
			Trigger:    toolexecution.EndResponseScope,
		}),
	)
	if err == nil || !strings.Contains(err.Error(), `subagent "researcher"`) || !strings.Contains(err.Error(), "EndResponseScope") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestProjectSkillIsSelectedByModelAndRetainedAsToolResult(t *testing.T) {
	project, err := LoadProject(projectFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "skill-call", Name: SkillLoaderToolName, Arguments: map[string]any{"name": "testing-go"},
	}}}
	agent, err := New(context.Background(), WithProject(project), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("skill-selection"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	requests := model.Requests()
	if len(requests) != 2 || len(requests[0].SystemPrompts) != 3 {
		t.Fatalf("model requests = %#v", requests)
	}
	if len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != SkillLoaderToolName {
		t.Fatalf("available tools = %#v", requests[0].Tools)
	}
	foundInstructions := false
	for _, message := range requests[1].Messages {
		if message.Type == agentruntime.MessageTypeToolResult && message.ToolResult != nil && strings.Contains(string(message.ToolResult.Output), "Run go test ./...") {
			foundInstructions = true
		}
	}
	if !foundInstructions {
		t.Fatalf("skill instructions were not returned to the model: %#v", requests[1].Messages)
	}
	messages, err := agent.ListMessages(context.Background(), "skill-selection")
	if err != nil {
		t.Fatal(err)
	}
	if got := messageTypes(messages); !slices.Equal(got, []agentruntime.MessageType{
		agentruntime.MessageTypeUser, agentruntime.MessageTypeToolCall,
		agentruntime.MessageTypeToolResult, agentruntime.MessageTypeAssistant,
	}) {
		t.Fatalf("message types = %v", got)
	}
}

func TestProjectSkillToolDeduplicatesRecentAndRefreshesStaleHistory(t *testing.T) {
	tests := []struct {
		name                string
		policy              SkillReloadPolicy
		wantSecondInContext bool
		wantBodies          int
	}{
		{
			name:                "recent instructions return lightweight result",
			policy:              SkillReloadPolicy{MaxTurnDistance: 2},
			wantSecondInContext: true,
			wantBodies:          1,
		},
		{
			name:       "stale instructions return a new full result",
			policy:     SkillReloadPolicy{MaxTurnDistance: 1},
			wantBodies: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project, err := LoadProject(projectFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			model := &skillEveryTurnModel{}
			agent, err := New(context.Background(),
				WithProject(project),
				WithSkillReloadPolicy(test.policy),
				WithModel(model),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()

			for _, turnID := range []string{"turn-1", "turn-2"} {
				run, err := agent.Start(context.Background(), agentruntime.Request{
					SessionID: "skill-history", TurnID: turnID,
					Message: agentruntime.Message{Type: agentruntime.MessageTypeUser, Content: "test the Go code"},
				})
				if err != nil {
					t.Fatal(err)
				}
				waitRun(t, run)
			}

			messages, err := agent.ListMessages(context.Background(), "skill-history")
			if err != nil {
				t.Fatal(err)
			}
			var results []skillToolResult
			for _, message := range messages {
				if message.Type != agentruntime.MessageTypeToolResult || message.ToolResult == nil || message.ToolResult.Name != SkillLoaderToolName {
					continue
				}
				var result skillToolResult
				if err := json.Unmarshal(message.ToolResult.Output, &result); err != nil {
					t.Fatal(err)
				}
				results = append(results, result)
			}
			if len(results) != 2 || results[0].Status != "loaded" || results[1].Status != "loaded" ||
				results[1].InstructionsInContext != test.wantSecondInContext {
				t.Fatalf("skill results = %#v", results)
			}
			bodies := 0
			for _, result := range results {
				if result.Instructions != "" {
					bodies++
				}
			}
			if bodies != test.wantBodies {
				t.Fatalf("full instruction bodies = %d, want %d", bodies, test.wantBodies)
			}
		})
	}
}

func TestLoadProjectValidatesConfigAndSkillMetadata(t *testing.T) {
	t.Run("unknown config field", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers: {openai: {type: openai, api_key: key}}\nunknown: true\n")
		if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "field unknown") {
			t.Fatalf("error = %v", err)
		}
	})

	tests := []struct {
		name      string
		directory string
		contents  string
	}{
		{name: "extra metadata", directory: "testing-go", contents: "---\nname: testing-go\ndescription: Tests Go\ntools: [bash]\n---\nBody\n"},
		{name: "invalid name", directory: "Testing_Go", contents: "---\nname: Testing_Go\ndescription: Tests Go\n---\nBody\n"},
		{name: "directory mismatch", directory: "different", contents: "---\nname: testing-go\ndescription: Tests Go\n---\nBody\n"},
		{name: "missing instructions", directory: "testing-go", contents: "---\nname: testing-go\ndescription: Tests Go\n---\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers: {openai: {type: openai, api_key: key}}\n")
			writeMainAgentDefinition(t, root, "openai", "model", "")
			writeTestFile(t, filepath.Join(root, ".agentcli", "skill", test.directory, "SKILL.md"), test.contents)
			if _, err := LoadProject(root); err == nil {
				t.Fatal("invalid skill unexpectedly loaded")
			}
		})
	}
}

func TestLoadProjectRequiresMainDefinitionAndRejectsLegacyAgentConfig(t *testing.T) {
	t.Run("missing MAIN", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers: {openai: {type: openai, api_key: key}}\n")
		if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "MAIN.md") {
			t.Fatalf("missing MAIN error = %v", err)
		}
	})

	t.Run("identity fields belong only to subagents", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers: {openai: {type: openai, api_key: key}}\n")
		writeTestFile(t, filepath.Join(root, ".agentcli", "MAIN.md"), "---\nname: main\nprovider: openai\nmodel: model\n---\nInstructions.\n")
		if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "field name") {
			t.Fatalf("main identity field error = %v", err)
		}
	})

	t.Run("legacy agent config", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "agent: {provider: openai, model: model, skills: [], tools: []}\nproviders: {openai: {type: openai, api_key: key}}\n")
		writeMainAgentDefinition(t, root, "openai", "model", "")
		if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "field agent") {
			t.Fatalf("legacy agent config error = %v", err)
		}
	})
}

func TestLoadProjectExpandsProviderEnvironmentAndDefaults(t *testing.T) {
	t.Setenv("PROJECT_TEST_API_KEY", "secret")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers:\n  local:\n    type: openai\n    url: https://example.test/v1\n    api_key: ${PROJECT_TEST_API_KEY}\n")
	writeMainAgentDefinition(t, root, "local", "model", "")
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if project.ProviderName() != "local" || project.PermissionMode() != permission.Default || len(project.SystemPrompts()) != 2 {
		t.Fatalf("project defaults = provider %q mode %q prompts %v", project.ProviderName(), project.PermissionMode(), project.SystemPrompts())
	}
	if prompt := project.SystemPrompts()[0]; !strings.Contains(prompt, `provider: "local"`) || !strings.Contains(prompt, `model: "model"`) || !strings.Contains(prompt, "# Runtime context") {
		t.Fatalf("main runtime prompt = %q", prompt)
	}
}

func TestLoadProjectLoggingConfigDefaultsAndValidation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `logging:
  level: debug
providers:
  local:
    type: openai
    api_key: key
`)
	writeMainAgentDefinition(t, root, "local", "model", "")
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if project.config.Logging == nil || !project.config.Logging.Enabled || project.config.Logging.Level != "debug" {
		t.Fatalf("logging config = %#v", project.config.Logging)
	}
	configuration := defaultConfig(root)
	if err := WithProject(project)(&configuration); err != nil {
		t.Fatal(err)
	}
	if configuration.logger == nil {
		t.Fatal("enabled project logging did not configure a logger")
	}

	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `logging:
  enabled: false
  level: info
providers:
  local:
    type: openai
    api_key: key
`)
	project, err = LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	configuration = defaultConfig(root)
	if err := WithProject(project)(&configuration); err != nil {
		t.Fatal(err)
	}
	if configuration.logger != nil {
		t.Fatal("disabled project logging configured a logger")
	}

	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `logging:
  level: trace
providers:
  local:
    type: openai
    api_key: key
`)
	if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "logging level") {
		t.Fatalf("LoadProject invalid logging error = %v", err)
	}
}

func TestLoadProjectExpandsAndValidatesLangfuseObservability(t *testing.T) {
	t.Setenv("TEST_LANGFUSE_BASE_URL", "https://us.cloud.langfuse.com")
	t.Setenv("TEST_LANGFUSE_PUBLIC_KEY", "pk-test")
	t.Setenv("TEST_LANGFUSE_SECRET_KEY", "sk-test")
	t.Setenv("TEST_APP_VERSION", "v1.2.3")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `observability:
  langfuse:
    enabled: true
    base_url: ${TEST_LANGFUSE_BASE_URL}
    public_key: ${TEST_LANGFUSE_PUBLIC_KEY}
    secret_key: ${TEST_LANGFUSE_SECRET_KEY}
    environment: testing
    service_name: agentcli-test
    release: ${TEST_APP_VERSION}
    sample_rate: 0.25
    capture:
      input: true
      output: true
      reasoning: false
providers:
  openai:
    type: openai
    api_key: key
`)
	writeMainAgentDefinition(t, root, "openai", "model", "")
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	config := project.config.Observability.Langfuse
	if config.BaseURL != "https://us.cloud.langfuse.com" || config.PublicKey != "pk-test" ||
		config.SecretKey != "sk-test" || config.Release != "v1.2.3" {
		t.Fatalf("expanded Langfuse config = %#v", config)
	}
	if config.SampleRate == nil || *config.SampleRate != 0.25 {
		t.Fatalf("sample rate = %v", config.SampleRate)
	}
	if !config.Capture.Input || !config.Capture.Output || config.Capture.Reasoning {
		t.Fatalf("capture config = %#v", config.Capture)
	}
}

func TestLoadProjectRejectsInvalidLangfuseObservability(t *testing.T) {
	for _, test := range []struct {
		name   string
		config string
		want   string
	}{
		{name: "missing public key", config: "enabled: true\n    secret_key: sk", want: "public_key"},
		{name: "missing secret key", config: "enabled: true\n    public_key: pk", want: "secret_key"},
		{name: "invalid base URL", config: "enabled: true\n    public_key: pk\n    secret_key: sk\n    base_url: ftp://example.test", want: "base_url"},
		{name: "invalid sample rate", config: "enabled: true\n    public_key: pk\n    secret_key: sk\n    sample_rate: 1.1", want: "sample_rate"},
		{name: "invalid environment", config: "enabled: true\n    public_key: pk\n    secret_key: sk\n    environment: Production", want: "environment"},
		{name: "reserved environment", config: "enabled: true\n    public_key: pk\n    secret_key: sk\n    environment: langfuse-prod", want: "environment"},
		{name: "unknown capture field", config: "enabled: false\n    capture:\n      prompt: true", want: "field prompt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "observability:\n  langfuse:\n    "+test.config+"\nproviders:\n  openai:\n    type: openai\n    api_key: key\n")
			writeMainAgentDefinition(t, root, "openai", "model", "")
			if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadProject error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProjectProviderReasoningSettingReachesModelRequest(t *testing.T) {
	var requestBody struct {
		ChatTemplateKwargs map[string]bool `json:"chat_template_kwargs"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `providers:
  local:
    type: openai
    url: `+server.URL+`
    api_key: test-key
    models:
      qwen-test:
        reasoning: false
`)
	writeMainAgentDefinition(t, root, "local", "qwen-test", "")

	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	model, err := project.Model()
	if err != nil {
		t.Fatal(err)
	}
	stream, err := model.Start(context.Background(), agentruntime.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Subscribe(context.Background()) {
	}
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := requestBody.ChatTemplateKwargs["enable_thinking"]; !ok || enabled {
		t.Fatalf("chat template kwargs = %#v, want enable_thinking false", requestBody.ChatTemplateKwargs)
	}
}

func TestProjectProviderExtraBodyReachesModelRequest(t *testing.T) {
	t.Setenv("PROJECT_TEST_THINKING_TYPE", "disabled")
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `providers:
  local:
    type: openai
    url: `+server.URL+`
    api_key: test-key
    models:
      provider-specific-model-alias:
        extra_body:
          thinking:
            type: ${PROJECT_TEST_THINKING_TYPE}
          provider_options:
            flags: [one, true, 3]
`)
	writeMainAgentDefinition(t, root, "local", "provider-specific-model-alias", "")

	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	model, err := project.Model()
	if err != nil {
		t.Fatal(err)
	}
	stream, err := model.Start(context.Background(), agentruntime.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Subscribe(context.Background()) {
	}
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}

	thinking, ok := requestBody["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", requestBody["thinking"])
	}
	options, ok := requestBody["provider_options"].(map[string]any)
	if !ok || len(options["flags"].([]any)) != 3 {
		t.Fatalf("provider_options = %#v", requestBody["provider_options"])
	}

	requestBody = nil
	unlistedModel, err := project.ModelFor("local", "unlisted-model")
	if err != nil {
		t.Fatal(err)
	}
	stream, err = unlistedModel.Start(context.Background(), agentruntime.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Subscribe(context.Background()) {
	}
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
	if _, found := requestBody["thinking"]; found {
		t.Fatalf("unlisted model unexpectedly received thinking override: %#v", requestBody)
	}
	if _, found := requestBody["provider_options"]; found {
		t.Fatalf("unlisted model unexpectedly received provider_options override: %#v", requestBody)
	}
}

func TestWithProviderStepLimitEnforcesTextFinalization(t *testing.T) {
	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `permission_mode: criticalOnly
providers:
  openai:
    type: openai
    api_key: test-key
`)
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	configuration := defaultConfig(root)
	if err := WithProject(project)(&configuration); err != nil {
		t.Fatal(err)
	}
	if configuration.maxProviderSteps != 0 {
		t.Fatalf("project implicitly enabled provider step limit %d", configuration.maxProviderSteps)
	}

	model := &scriptedModel{toolCalls: []provider.ToolCall{{
		ID: "skill-call", Name: SkillLoaderToolName, Arguments: map[string]any{"name": "testing-go"},
	}}}
	agent, err := New(context.Background(), WithProject(project), WithModel(model), WithProviderStepLimit(1))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	run, err := agent.Start(context.Background(), userRequest("max-provider-steps"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	result, err := run.Result()
	if err != nil || result.Content != "done" || !run.StepLimitFinalized() {
		t.Fatalf("run result = (%#v, %v), finalized=%v", result, err, run.StepLimitFinalized())
	}
	requests := model.Requests()
	if len(requests) != 2 || len(requests[1].Tools) != 0 {
		t.Fatalf("provider requests = %#v, want tools-disabled finalizer", requests)
	}
}

func TestLoadProjectRejectsProviderStepLimitYAML(t *testing.T) {
	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `max_provider_steps: 20
providers:
  openai:
    type: openai
    api_key: test-key
`)
	if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "field max_provider_steps not found") {
		t.Fatalf("LoadProject() error = %v, want unsupported max_provider_steps field", err)
	}
}

func TestLoadProjectRejectsRemovedMaxSubagentsField(t *testing.T) {
	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `max_subagents: -1
providers:
  openai:
    type: openai
    api_key: test-key
`)
	if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "field max_subagents not found") {
		t.Fatalf("LoadProject() error = %v, want removed max_subagents field", err)
	}
}

func TestLoadProjectCompactionConfigDefaultsAndResolvesModel(t *testing.T) {
	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `compaction:
  provider: openai
  model: compact-model
providers:
  openai:
    type: openai
    api_key: test-key
    models:
      compact-model:
        context_window_tokens: 8192
        max_output_tokens: 1024
`)
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	compaction, configured := project.Compaction()
	if !configured || !compaction.Auto || compaction.Provider != "openai" || compaction.Model != "compact-model" {
		t.Fatalf("compaction = %#v, configured = %t", compaction, configured)
	}
	compaction.Provider = "mutated"
	if current, _ := project.Compaction(); current.Provider != "openai" {
		t.Fatal("compaction config was not defensively copied")
	}
	configuration := defaultConfig(root)
	if err := WithProject(project)(&configuration); err != nil {
		t.Fatal(err)
	}
	if configuration.compactionModel == nil {
		t.Fatal("WithProject did not resolve the compaction model")
	}
}

func TestPublicCompactionOptionsOverrideProjectDefaults(t *testing.T) {
	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `compaction:
  provider: openai
  model: compact-model
providers:
  openai:
    type: openai
    api_key: test-key
    models:
      compact-model:
        context_window_tokens: 8192
        max_output_tokens: 1024
`)
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{}
	estimator := agentruntime.ContextEstimatorFunc(func(agentruntime.ModelRequest) (agentruntime.ContextEstimate, error) {
		return agentruntime.ContextEstimate{Tokens: 123}, nil
	})

	configuration := defaultConfig(root)
	if err := WithProject(project)(&configuration); err != nil {
		t.Fatal(err)
	}
	projectModel := configuration.compactionModel
	if projectModel == nil {
		t.Fatal("WithProject did not set its compaction default")
	}
	if err := WithCompactionModel(model)(&configuration); err != nil {
		t.Fatal(err)
	}
	if err := WithContextEstimator(estimator)(&configuration); err != nil {
		t.Fatal(err)
	}
	if configuration.compactionModel != model {
		t.Fatal("WithCompactionModel did not override the project default")
	}
	estimate, err := configuration.contextEstimator.Estimate(agentruntime.ModelRequest{})
	if err != nil || estimate.Tokens != 123 {
		t.Fatalf("WithContextEstimator estimate = %#v, %v", estimate, err)
	}

	configuration = defaultConfig(root)
	if err := WithCompactionModel(model)(&configuration); err != nil {
		t.Fatal(err)
	}
	if err := WithProject(project)(&configuration); err != nil {
		t.Fatal(err)
	}
	if configuration.compactionModel == model {
		t.Fatal("later WithProject did not win")
	}
}

func TestPublicCompactionOptionsRejectNil(t *testing.T) {
	configuration := defaultConfig(t.TempDir())
	if err := WithCompactionModel(nil)(&configuration); err == nil || !strings.Contains(err.Error(), "compaction model is required") {
		t.Fatalf("WithCompactionModel(nil) error = %v", err)
	}
	if err := WithContextEstimator(nil)(&configuration); err == nil || !strings.Contains(err.Error(), "context estimator is required") {
		t.Fatalf("WithContextEstimator(nil) error = %v", err)
	}
	var typedNilModel *scriptedModel
	if err := WithCompactionModel(typedNilModel)(&configuration); err == nil || !strings.Contains(err.Error(), "compaction model is required") {
		t.Fatalf("WithCompactionModel(typed nil) error = %v", err)
	}
	var typedNilEstimator *typedNilContextEstimator
	if err := WithContextEstimator(typedNilEstimator)(&configuration); err == nil || !strings.Contains(err.Error(), "context estimator is required") {
		t.Fatalf("WithContextEstimator(typed nil) error = %v", err)
	}
}

type typedNilContextEstimator struct{}

func (*typedNilContextEstimator) Estimate(agentruntime.ModelRequest) (agentruntime.ContextEstimate, error) {
	return agentruntime.ContextEstimate{}, nil
}

func TestLoadProjectCompactionConfigValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		yaml string
		want string
	}{
		{name: "missing provider", yaml: "compaction: {model: compact-model}\n", want: "compaction provider is required"},
		{name: "missing model", yaml: "compaction: {provider: openai}\n", want: "compaction model is required"},
		{name: "unknown provider", yaml: "compaction: {provider: missing, model: compact-model}\n", want: `compaction provider "missing" is not configured`},
		{name: "unknown field", yaml: "compaction: {provider: openai, model: compact-model, budget: 1}\n", want: "field budget"},
		{name: "legacy metadata field", yaml: "compaction: {provider: openai, model: compact-model, context_window_tokens: 10}\n", want: "field context_window_tokens"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := projectFixture(t)
			writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), test.yaml+`providers:
  openai:
    type: openai
    api_key: test-key
`)
			if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadProject() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadProjectExplicitlyDisabledCompactionNeedsNoModel(t *testing.T) {
	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `compaction:
  auto: false
providers:
  openai:
    type: openai
    api_key: test-key
`)
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	compaction, configured := project.Compaction()
	if !configured || compaction.Auto {
		t.Fatalf("compaction = %#v, configured = %t", compaction, configured)
	}
	configuration := defaultConfig(root)
	if err := WithProject(project)(&configuration); err != nil {
		t.Fatal(err)
	}
	if configuration.compactionModel != nil {
		t.Fatal("disabled compaction unexpectedly resolved a model")
	}
}

func TestLoadProjectRequiresSupportedProviderTypeIndependentOfAlias(t *testing.T) {
	for _, test := range []struct {
		name        string
		provider    string
		want        string
		wantNoError bool
	}{
		{name: "arbitrary alias selects openai by type", provider: "type: openai\n    api_key: key", wantNoError: true},
		{name: "model metadata", provider: "type: openai\n    api_key: key\n    models:\n      model:\n        context_window_tokens: 131072\n        max_output_tokens: 16384", wantNoError: true},
		{name: "legacy provider metadata", provider: "type: openai\n    api_key: key\n    context_window_tokens: 131072\n    max_output_tokens: 16384", want: "field context_window_tokens"},
		{name: "legacy provider extra body", provider: "type: openai\n    api_key: key\n    extra_body: {thinking: {type: disabled}}", want: "field extra_body"},
		{name: "missing type", provider: "api_key: key", want: `provider "custom-profile" type is required`},
		{name: "unsupported type", provider: "type: anthropic\n    api_key: key", want: `provider "custom-profile" has unsupported type "anthropic"`},
		{name: "metadata missing context", provider: "type: openai\n    api_key: key\n    models:\n      model:\n        max_output_tokens: 1024", want: `provider "custom-profile" model "model" metadata`},
		{name: "metadata output exceeds context", provider: "type: openai\n    api_key: key\n    models:\n      model:\n        context_window_tokens: 10\n        max_output_tokens: 11", want: "maximum output tokens cannot exceed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers:\n  custom-profile:\n    "+test.provider+"\n")
			writeMainAgentDefinition(t, root, "custom-profile", "model", "")
			project, err := LoadProject(root)
			if test.wantNoError {
				if err != nil {
					t.Fatal(err)
				}
				if project.ProviderName() != "custom-profile" {
					t.Fatalf("provider alias = %q", project.ProviderName())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadProject() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateProviderConfigRejectsNonJSONExtraBody(t *testing.T) {
	_, err := validateProviderConfig("custom-profile", ProviderConfig{
		Type:   ProviderTypeOpenAI,
		APIKey: "key",
		Models: map[string]ProviderModelConfig{
			"selected": {ExtraBody: map[string]any{"invalid": func() {}}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "extra_body must contain valid JSON values") {
		t.Fatalf("validateProviderConfig() error = %v", err)
	}
}

func TestMainDefinitionSelectsRootModelSkillsAndTools(t *testing.T) {
	root := projectFixture(t)
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `permission_mode: criticalOnly
providers:
  openai:
    type: openai
    url: https://example.test/v1
    api_key: test-key
    request_timeout: 30s
`)
	writeMainAgentDefinition(t, root, "openai", "root-model", "skills: [testing-go]\ntools: [search]")
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if project.ProviderName() != "openai" || project.ModelName() != "root-model" || !slices.Equal(project.ToolNames(), []string{"search"}) {
		t.Fatalf("main-agent selection = provider %q model %q tools %v", project.ProviderName(), project.ModelName(), project.ToolNames())
	}
	if skills := project.Skills(); len(skills) != 1 || skills[0].Name != "testing-go" {
		t.Fatalf("root skills = %#v", skills)
	}
	if _, err := New(context.Background(), WithProject(project), WithModel(&scriptedModel{})); err == nil || !strings.Contains(err.Error(), `main agent requires custom tool "search"`) {
		t.Fatalf("missing main-agent tool error = %v", err)
	}

	model := &scriptedModel{}
	agent, err := New(context.Background(),
		WithProject(project), WithModel(model),
		WithTool(testTool("write")), WithTool(testTool("search")),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	run, err := agent.Start(context.Background(), userRequest("root-agent-config"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	requests := model.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %#v", requests)
	}
	names := make([]string, len(requests[0].Tools))
	for index, tool := range requests[0].Tools {
		names[index] = tool.Name
	}
	if !slices.Equal(names, []string{"search", SkillLoaderToolName}) {
		t.Fatalf("root provider tools = %v", names)
	}
}

func TestMainDefinitionRejectsUnknownSkillsAndDuplicateTools(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields string
		want   string
	}{
		{name: "unknown skill", fields: "skills: [missing]", want: `skill "missing" is not available`},
		{name: "duplicate tool", fields: "tools: [search, search]", want: `duplicate tool "search"`},
		{name: "framework task tool", fields: "tools: [task]", want: "task is a framework tool"},
		{name: "explicit empty skills", fields: "skills: []", want: `remove skills when no skills are allowed`},
		{name: "explicit empty tools", fields: "tools: []", want: `remove tools when no tools are allowed`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := projectFixture(t)
			writeMainAgentDefinition(t, root, "openai", "root-model", test.fields)
			if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadProject error = %v, want %q", err, test.want)
			}
		})
	}
}

func projectFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("PROJECT_FIXTURE_API_KEY", "test-key")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), `permission_mode: criticalOnly
providers:
  openai:
    type: openai
    url: https://example.test/v1
    api_key: ${PROJECT_FIXTURE_API_KEY}
    request_timeout: 30s
`)
	writeTestFile(t, filepath.Join(root, "AGENTS.md"), "Always explain failures clearly.\n")
	writeMainAgentDefinition(t, root, "openai", "gpt-test", "skills: [reviewing-go, testing-go]")
	writeTestFile(t, filepath.Join(root, ".agentcli", "skill", "testing-go", "SKILL.md"), `---
name: testing-go
description: Runs and diagnoses Go tests; use when Go tests are requested or failing.
---
# Testing Go

Run go test ./... and explain any failure.
`)
	writeTestFile(t, filepath.Join(root, ".agentcli", "skill", "reviewing-go", "SKILL.md"), `---
name: reviewing-go
description: Reviews Go code for correctness; use for Go code-review requests.
---
# Reviewing Go

Inspect concurrency and error handling.
`)
	return root
}

func writeMainAgentDefinition(t *testing.T, root, provider, model, capabilities string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, ".agentcli", "MAIN.md"), "---\nprovider: "+provider+"\nmodel: "+model+"\n"+capabilities+"\n---\n\nCoordinate work and communicate the outcome clearly.\n")
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

type skillEveryTurnModel struct {
	mu       sync.Mutex
	requests []agentruntime.ModelRequest
}

func (model *skillEveryTurnModel) Start(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	model.mu.Lock()
	model.requests = append(model.requests, request)
	model.mu.Unlock()

	loadedThisTurn := false
	for _, message := range request.Messages {
		if message.TurnID != request.TurnID || message.Type != agentruntime.MessageTypeToolResult || message.ToolResult == nil {
			continue
		}
		if message.ToolResult.Name == SkillLoaderToolName {
			loadedThisTurn = true
			break
		}
	}
	if !loadedThisTurn {
		return scriptedStream{result: provider.StreamResult{
			CompletedTools: []provider.ToolCall{{
				ID: "skill-" + request.TurnID, Name: SkillLoaderToolName,
				Arguments: map[string]any{"name": "testing-go"},
			}},
			Finished: true,
		}}, nil
	}
	return scriptedStream{result: provider.StreamResult{Content: "done", Finished: true}}, nil
}
