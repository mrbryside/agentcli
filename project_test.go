package agentcli

import (
	"context"
	"encoding/json"
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

func TestLoadProjectBuildsGroupedFrameworkPromptAndProgressiveSkillLoader(t *testing.T) {
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
	if len(prompts) != 2 {
		t.Fatalf("system prompts = %#v", prompts)
	}
	if !strings.Contains(prompts[0], "# Runtime context") || !strings.Contains(prompts[0], `agent: "main"`) || !strings.Contains(prompts[0], `provider: "openai"`) || !strings.Contains(prompts[0], `model: "gpt-test"`) || !strings.Contains(prompts[0], `working_directory: "`+root+`"`) {
		t.Fatalf("main runtime context = %q", prompts[0])
	}
	if !strings.Contains(prompts[0], "# Main agent instructions") || !strings.Contains(prompts[0], "Coordinate work and communicate the outcome clearly.") {
		t.Fatalf("main instructions = %q", prompts[0])
	}
	if !strings.Contains(prompts[0], "# Sensitive information") || !strings.Contains(prompts[0], modelSecretSafetyPrompt) {
		t.Fatalf("main secret-safety prompt = %q", prompts[0])
	}
	if !strings.Contains(prompts[0], "<name>testing-go</name>") || !strings.Contains(prompts[0], "when Go tests are requested") {
		t.Fatalf("skill discovery prompt = %q", prompts[0])
	}
	if !strings.Contains(prompts[0], "discovery-only") || !strings.Contains(prompts[0], "MUST NOT call load_skill") {
		t.Fatalf("skill discovery prompt does not prevent listing from loading a skill: %q", prompts[0])
	}
	if strings.Contains(prompts[0], "Run go test ./...") {
		t.Fatalf("skill body was eagerly loaded in discovery prompt: %q", prompts[0])
	}
	if prompts[1] != "Always explain failures clearly.\n" {
		t.Fatalf("AGENTS prompt = %q", prompts[1])
	}

	configuration := defaultConfig(root)
	if err := WithProject(project)(&configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.systemPrompts) != 2 || len(configuration.tools) != 0 || configuration.project != project {
		t.Fatalf("applied project = prompts %d tools %#v project %p", len(configuration.systemPrompts), configuration.tools, configuration.project)
	}
	tool := newSkillLoader(project, configuration.messages, configuration.skillReload).tool()
	if !strings.Contains(tool.Definition.Description, "Do not call this tool to list available skills") {
		t.Fatalf("load_skill description does not protect discovery-only requests: %q", tool.Definition.Description)
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
	if len(requests) != 2 || len(requests[0].SystemPrompts) != 2 {
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
		name             string
		policy           SkillReloadPolicy
		wantSecondStatus string
		wantBodies       int
	}{
		{
			name:             "recent instructions return lightweight result",
			policy:           SkillReloadPolicy{MaxTurnDistance: 2},
			wantSecondStatus: "already_loaded",
			wantBodies:       1,
		},
		{
			name:             "stale instructions return a new full result",
			policy:           SkillReloadPolicy{MaxTurnDistance: 1},
			wantSecondStatus: "loaded",
			wantBodies:       2,
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
			if len(results) != 2 || results[0].Status != "loaded" || results[1].Status != test.wantSecondStatus {
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
	if project.ProviderName() != "local" || project.PermissionMode() != permission.Default || len(project.SystemPrompts()) != 1 {
		t.Fatalf("project defaults = provider %q mode %q prompts %v", project.ProviderName(), project.PermissionMode(), project.SystemPrompts())
	}
	if prompt := project.SystemPrompts()[0]; !strings.Contains(prompt, `provider: "local"`) || !strings.Contains(prompt, `model: "model"`) || !strings.Contains(prompt, "# Runtime context") {
		t.Fatalf("main runtime prompt = %q", prompt)
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
		{name: "missing type", provider: "api_key: key", want: `provider "custom-profile" type is required`},
		{name: "unsupported type", provider: "type: anthropic\n    api_key: key", want: `provider "custom-profile" has unsupported type "anthropic"`},
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
		t.Fatalf("root agent selection = provider %q model %q tools %v", project.ProviderName(), project.ModelName(), project.ToolNames())
	}
	if skills := project.Skills(); len(skills) != 1 || skills[0].Name != "testing-go" {
		t.Fatalf("root skills = %#v", skills)
	}
	if _, err := New(context.Background(), WithProject(project), WithModel(&scriptedModel{})); err == nil || !strings.Contains(err.Error(), `root agent requires custom tool "search"`) {
		t.Fatalf("missing root tool error = %v", err)
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
