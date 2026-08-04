package agentcli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/toolexecution"
)

func TestLoadProjectSubagentDefinitions(t *testing.T) {
	root := projectFixture(t)
	writeSubagentDefinition(t, root, "reviewer", `---
name: reviewer
description: Review proposed changes carefully.
provider: openai
model: gpt-review
---
Return concrete findings only.
`)
	writeSubagentDefinition(t, root, "researcher", `---
name: researcher
description: Research alternatives and trade-offs.
provider: openai
model: gpt-research
skills:
  - testing-go
tools:
  - search
---
Use sources and explain uncertainty.
`)

	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	definitions := project.subagentDefinitions()
	if names := []string{definitions[0].Name, definitions[1].Name}; !slices.Equal(names, []string{"researcher", "reviewer"}) {
		t.Fatalf("definition names = %v", names)
	}
	if definitions[0].Description != "Research alternatives and trade-offs." || definitions[0].Provider != "openai" || definitions[0].Model != "gpt-research" {
		t.Fatalf("researcher metadata = %#v", definitions[0])
	}
	if !slices.Equal(definitions[0].Skills, []string{"testing-go"}) || len(definitions[1].Skills) != 0 {
		t.Fatalf("definition skills = %#v", definitions)
	}
	if !slices.Equal(definitions[0].Tools, []string{"search"}) || len(definitions[1].Tools) != 0 {
		t.Fatalf("definition tools = %#v", definitions)
	}
	if definitions[0].Instructions != "Use sources and explain uncertainty." {
		t.Fatalf("researcher instructions = %q", definitions[0].Instructions)
	}
	if definitions[0].Path != filepath.Join(root, ".agentcli", "agent", "researcher", "researcher.md") {
		t.Fatalf("definition path = %q", definitions[0].Path)
	}

	prompts := project.systemPrompts()
	if len(prompts) != 4 {
		t.Fatalf("system prompts = %#v", prompts)
	}
	framework := prompts[0]
	skills := prompts[1]
	subagents := prompts[2]
	mainInstructions := prompts[3]
	if strings.Contains(framework, "# Subagents") ||
		strings.Contains(framework, "<subagent_orchestration_rules>") ||
		strings.Contains(framework, "<available_subagents>") ||
		strings.Contains(framework, "# Skills") ||
		strings.Contains(framework, "<available_skills>") {
		t.Fatalf("framework prompt still contains capability discovery material: %q", framework)
	}
	if !strings.Contains(subagents, "# Subagents") ||
		!strings.Contains(subagents, "Use task for focused work") ||
		!strings.Contains(subagents, "Foreground is the default") ||
		!strings.Contains(subagents, "same tool batch") ||
		!strings.Contains(subagents, "task_id") {
		t.Fatalf("task prompt = %q", subagents)
	}
	if mainInstructions != "# Main agent instructions\n\nCoordinate work and communicate the outcome clearly." {
		t.Fatalf("MAIN.md system prompt = %q", mainInstructions)
	}
	if !strings.Contains(skills, "# Skills") || !strings.Contains(skills, "<available_skills>") || !strings.Contains(skills, "<name>testing-go</name>") {
		t.Fatalf("skill prompt does not contain skill catalog: %q", skills)
	}
	if !strings.Contains(subagents, "<available_task_agents>") || !strings.Contains(subagents, "<name>researcher</name>") || !strings.Contains(subagents, "<description>Research alternatives and trade-offs.</description>") {
		t.Fatalf("subagent catalog = %q", subagents)
	}
	for _, expected := range []string{
		"Foreground",
		"same tool batch",
		"different companies, regions, years, or sources",
		"task_id",
		"background",
		"<available_task_agents>",
		"</available_task_agents>",
	} {
		if !strings.Contains(subagents, expected) {
			t.Fatalf("catalog does not contain subagent rule %q: %q", expected, subagents)
		}
	}
	if strings.Contains(subagents, "close_subagent") {
		t.Fatalf("removed destructive tool still appears in the model system prompt: %q", subagents)
	}
	for _, obsolete := range []string{
		"callback", "continue_main_agent", "accepted", "result_progress",
		"report_subagent_result", "send_subagent_message", "polling",
		"simulated waiting",
	} {
		if strings.Contains(strings.ToLower(subagents), obsolete) {
			t.Fatalf("subagent prompt contains obsolete model-facing term %q: %q", obsolete, subagents)
		}
	}
	for _, unavailable := range []string{"<model>", "<skill>", "<tool>", "Use sources and explain uncertainty."} {
		if strings.Contains(subagents, unavailable) {
			t.Fatalf("subagent catalog exposes implementation detail %q: %q", unavailable, subagents)
		}
	}
	if strings.Contains(subagents, "Use sources and explain uncertainty.") {
		t.Fatalf("definition instructions were eagerly exposed: %q", subagents)
	}
	if strings.Contains(strings.Join(prompts, "\n"), "Always explain failures clearly.") {
		t.Fatalf("AGENTS.md was included in main system prompts: %#v", prompts)
	}
}

func TestPublicSubagentDefinitionContainsDiscoveryMetadataOnly(t *testing.T) {
	typeOfDefinition := reflect.TypeFor[SubagentDefinition]()
	fields := make([]string, typeOfDefinition.NumField())
	for index := range fields {
		fields[index] = typeOfDefinition.Field(index).Name
	}
	want := []string{"Name", "Description", "Provider", "Model", "Skills", "Tools"}
	if !slices.Equal(fields, want) {
		t.Fatalf("public definition fields = %v, want %v", fields, want)
	}

	internal := agentDefinition{
		Name: "researcher", Description: "Researches.", Provider: "openai", Model: "gpt-test",
		Skills: []string{"testing-go"}, Tools: []string{"search"},
		Instructions: "private prompt", Path: "/private/definition.md",
	}
	public := publicSubagentDefinition(internal)
	public.Skills[0] = "changed"
	public.Tools[0] = "changed"
	if internal.Skills[0] != "testing-go" || internal.Tools[0] != "search" {
		t.Fatalf("public discovery metadata aliases private project slices: %#v", internal)
	}
}

func TestParseSubagentDefinitionNormalizesResultContract(t *testing.T) {
	definition, err := parseSubagentDefinition("researcher.md", []byte(`---
name: researcher
description: Research project files.
provider: openai
model: gpt-test
result:
  message_field: " message "
  metadata:
    " requires_requester_reply ":
      type: " boolean "
      required: true
    source:
      type: string
---
Return one final response.
`))
	if err != nil {
		t.Fatal(err)
	}
	if definition.Result == nil {
		t.Fatal("result contract = nil")
	}
	if definition.Result.MessageField != "message" {
		t.Fatalf("message field = %q", definition.Result.MessageField)
	}
	if got, want := definition.Result.Metadata, map[string]agentResultMetadataField{
		"requires_requester_reply": {Type: "boolean", Required: true},
		"source":                   {Type: "string"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
}

func TestParseSubagentDefinitionRejectsInvalidResultContracts(t *testing.T) {
	base := "---\nname: researcher\ndescription: Research project files.\nprovider: openai\nmodel: gpt-test\n%s---\nReturn one final response.\n"
	for _, test := range []struct {
		name   string
		result string
	}{
		{name: "empty message field", result: "result:\n  message_field: '   '\n"},
		{name: "metadata conflicts with message", result: "result:\n  message_field: message\n  metadata:\n    message:\n      type: string\n"},
		{name: "unsupported metadata type", result: "result:\n  message_field: message\n  metadata:\n    count:\n      type: number\n"},
		{name: "empty metadata field", result: "result:\n  message_field: message\n  metadata:\n    '   ':\n      type: boolean\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseSubagentDefinition("researcher.md", []byte(fmt.Sprintf(base, test.result))); err == nil {
				t.Fatal("invalid result contract unexpectedly loaded")
			}
		})
	}
}

func TestLoadProjectRejectsInvalidSubagentDefinitions(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		filename  string
		contents  string
		prepare   func(t *testing.T, root string)
	}{
		{
			name: "extra frontmatter field", directory: "researcher", filename: "researcher.md", contents: "---\nname: researcher\ndescription: Research\nprovider: openai\nmodel: gpt-test\nunexpected: true\n---\nBody\n",
		},
		{
			name: "missing required field", directory: "researcher", filename: "researcher.md", contents: "---\nname: researcher\ndescription: Research\nprovider: openai\n---\nBody\n",
		},
		{
			name: "name directory mismatch", directory: "researcher", filename: "researcher.md", contents: "---\nname: reviewer\ndescription: Research\nprovider: openai\nmodel: gpt-test\n---\nBody\n",
		},
		{
			name: "name filename mismatch", directory: "researcher", filename: "different.md", contents: "---\nname: researcher\ndescription: Research\nprovider: openai\nmodel: gpt-test\n---\nBody\n",
		},
		{
			name: "missing body", directory: "researcher", filename: "researcher.md", contents: "---\nname: researcher\ndescription: Research\nprovider: openai\nmodel: gpt-test\n---\n",
		},
		{
			name: "unknown provider", directory: "researcher", filename: "researcher.md", contents: "---\nname: researcher\ndescription: Research\nprovider: missing\nmodel: gpt-test\n---\nBody\n",
		},
		{
			name: "unknown skill", directory: "researcher", filename: "researcher.md", contents: "---\nname: researcher\ndescription: Research\nprovider: openai\nmodel: gpt-test\nskills: [missing]\n---\nBody\n",
		},
		{
			name: "duplicate skill", directory: "researcher", filename: "researcher.md", contents: "---\nname: researcher\ndescription: Research\nprovider: openai\nmodel: gpt-test\nskills: [testing-go, testing-go]\n---\nBody\n",
		},
		{
			name: "duplicate tool", directory: "researcher", filename: "researcher.md", contents: "---\nname: researcher\ndescription: Research\nprovider: openai\nmodel: gpt-test\ntools: [search, search]\n---\nBody\n",
		},
		{
			name: "explicit empty skills", directory: "researcher", filename: "researcher.md", contents: "---\nname: researcher\ndescription: Research\nprovider: openai\nmodel: gpt-test\nskills: []\n---\nBody\n",
		},
		{
			name: "explicit empty tools", directory: "researcher", filename: "researcher.md", contents: "---\nname: researcher\ndescription: Research\nprovider: openai\nmodel: gpt-test\ntools: []\n---\nBody\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := projectFixture(t)
			writeTestFile(t, filepath.Join(root, ".agentcli", "agent", test.directory, test.filename), test.contents)
			if _, err := LoadProject(root); err == nil {
				t.Fatal("invalid subagent definition unexpectedly loaded")
			}
		})
	}

	t.Run("symbolic link", func(t *testing.T) {
		root := projectFixture(t)
		path := filepath.Join(root, ".agentcli", "agent", "researcher", "researcher.md")
		writeTestFile(t, filepath.Join(root, "source.md"), "definition")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "source.md"), path); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProject(root); err == nil {
			t.Fatal("symbolic-link definition unexpectedly loaded")
		}
	})

	t.Run("oversized definition", func(t *testing.T) {
		root := projectFixture(t)
		writeSubagentDefinition(t, root, "researcher", "---\nname: researcher\ndescription: Research\nprovider: openai\nmodel: gpt-test\n---\n"+strings.Repeat("x", maxProjectFileSize))
		if _, err := LoadProject(root); err == nil {
			t.Fatal("oversized definition unexpectedly loaded")
		}
	})
}

func TestSubagentProjectContainsOnlyAllowedSkills(t *testing.T) {
	project, err := LoadProject(projectFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	subagent := project.withSkills([]string{"testing-go"})
	if skills := subagent.sortedSkills(); len(skills) != 1 || skills[0].Name != "testing-go" {
		t.Fatalf("subagent skills = %#v", skills)
	}
	prompts := subagent.systemPrompts()
	joinedPrompts := strings.Join(prompts, "\n")
	if len(prompts) == 0 || !strings.Contains(joinedPrompts, "testing-go") || strings.Contains(joinedPrompts, "reviewing-go") {
		t.Fatalf("subagent skill prompt = %#v", prompts)
	}
	withoutSkills := project.withSkills(nil)
	if len(withoutSkills.sortedSkills()) != 0 || strings.Contains(strings.Join(withoutSkills.systemPrompts(), "\n"), "available_skills") {
		t.Fatalf("subagent without skills = %#v", withoutSkills.systemPrompts())
	}
}

func TestSubagentSystemPromptsKeepAssignmentSeparateFromFramework(t *testing.T) {
	project, err := LoadProject(projectFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	subagentProject := project.withSkills([]string{"testing-go"})
	definition := agentDefinition{
		Name:         "researcher",
		Provider:     "openai",
		Model:        "gpt-research",
		Instructions: "Investigate the delegated question and report concrete findings.",
	}
	configuration := defaultConfig(project.root)
	if err := withSubagentSystemPrompts(subagentProject, definition)(&configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.systemPrompts) != 2 {
		t.Fatalf("subagent system prompts = %#v", configuration.systemPrompts)
	}
	framework := configuration.systemPrompts[0]
	for _, expected := range []string{
		`configured "researcher" subagent`,
		"# Runtime context",
		`agent: "researcher"`,
		`provider: "openai"`,
		`model: "gpt-research"`,
		`working_directory: "` + project.root + `"`,
		"# Evidence and tool use",
		subagentCapabilityBoundaryPrompt,
		"tools listed with the current request are available now",
		"authoritative tool list",
		"do not claim it is missing or ask the user to enable it",
		"# Sensitive information",
		modelSecretSafetyPrompt,
		"# Skills",
		"<name>testing-go</name>",
		"# Delivery contract",
		subagentCompletionPrompt,
		"Assignment instructions may describe only the domain goal",
		"this delivery contract still applies",
		"do not guess or take the action",
		"one exact question for the user",
		"same task can receive the user's answer later",
	} {
		if !strings.Contains(framework, expected) {
			t.Fatalf("subagent framework prompt does not contain %q: %q", expected, framework)
		}
	}
	if strings.Contains(framework, "# Assignment role") || strings.Contains(framework, definition.Instructions) {
		t.Fatalf("subagent framework prompt contains subagent instructions: %q", framework)
	}
	if strings.Contains(framework, "<name>reviewing-go</name>") || strings.Contains(framework, "Run go test ./...") {
		t.Fatalf("subagent framework prompt exposes disallowed skill or eager skill body: %q", framework)
	}
	if strings.Contains(framework, "Coordinate work and communicate the outcome clearly.") {
		t.Fatalf("subagent prompt contains main-only instructions: %q", framework)
	}
	if configuration.systemPrompts[1] != "# Assignment role\n\n"+definition.Instructions {
		t.Fatalf("subagent instruction system prompt = %q", configuration.systemPrompts[1])
	}
	if strings.Contains(strings.Join(configuration.systemPrompts, "\n"), "Always explain failures clearly.") {
		t.Fatalf("AGENTS.md was included in subagent system prompts: %#v", configuration.systemPrompts)
	}
}

func TestSubagentResultContractPromptIsOptInAndExact(t *testing.T) {
	project, err := LoadProject(projectFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	plain := subagentSystemPrompt(project, agentDefinition{Name: "reader", Provider: "openai", Model: "gpt-test"})
	if strings.Contains(plain, "# Final response format") || strings.Contains(plain, "exactly one JSON object") {
		t.Fatalf("plain subagent unexpectedly received result contract prompt: %q", plain)
	}

	definition := agentDefinition{
		Name: "operator", Provider: "openai", Model: "gpt-test",
		Result: &agentResultContract{
			MessageField: "message",
			Metadata: map[string]agentResultMetadataField{
				"requires_requester_reply": {Type: "boolean", Required: true},
				"source":                   {Type: "string"},
			},
		},
	}
	prompt := subagentSystemPrompt(project, definition)
	for _, expected := range []string{
		"# Final response format",
		"Return exactly one JSON object",
		`"message": required string`,
		"task result for the main agent",
		`"requires_requester_reply": required boolean`,
		`"source": optional string`,
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("result contract prompt missing %q: %q", expected, prompt)
		}
	}
}

func TestSubagentRuntimeRegistersSkillLoaderOnlyForAllowedSkills(t *testing.T) {
	project, err := LoadProject(projectFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		skills    []string
		wantTools int
	}{
		{name: "selected skill", skills: []string{"testing-go"}, wantTools: 1},
		{name: "no skills", wantTools: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &scriptedModel{}
			subagentProject := project.withSkills(test.skills)
			agent, err := New(context.Background(),
				withSubagentAgent(),
				withSubagentProject(subagentProject),
				withSubagentSystemPrompts(subagentProject, agentDefinition{Name: "researcher", Instructions: "Research the delegated task."}),
				WithModel(model),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()
			run, err := agent.Start(context.Background(), userRequest("subagent-skills"))
			if err != nil {
				t.Fatal(err)
			}
			waitRun(t, run)
			requests := model.Requests()
			if len(requests) < 1 || len(requests[0].Tools) != test.wantTools {
				t.Fatalf("provider tools = %#v", requests)
			}
			if len(requests[0].SystemPrompts) != 2 {
				t.Fatalf("provider system prompts = %#v", requests[0].SystemPrompts)
			}
			if len(test.skills) != 0 && requests[0].Tools[0].Name != skillLoaderToolName {
				t.Fatalf("provider tools = %#v", requests[0].Tools)
			}
			assertSubagentTextOnlyRequests(t, requests)
		})
	}
}

func TestAgentValidatesAndFiltersSubagentToolAllowlist(t *testing.T) {
	root := projectFixture(t)
	writeSubagentDefinition(t, root, "researcher", `---
name: researcher
description: Research project files.
provider: openai
model: gpt-test
tools: [search]
---
Use the registered search tool.
`)
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), WithProject(project), WithModel(&scriptedModel{})); err == nil || !strings.Contains(err.Error(), `requires custom tool "search"`) {
		t.Fatalf("missing tool error = %v", err)
	}

	rootAgent, err := New(context.Background(), WithProject(project), WithModel(&scriptedModel{}), WithTool(testTool("search")), WithTool(testTool("write")))
	if err != nil {
		t.Fatal(err)
	}
	defer rootAgent.Close()

	definition := project.subagentDefinitions()[0]
	selected := filterSubagentTools(definition, []toolexecution.Tool{testTool("write"), testTool("search")})
	if len(selected) != 1 || selected[0].Definition.Name != "search" {
		t.Fatalf("selected tools = %#v", selected)
	}
	model := &scriptedModel{}
	subagent, err := New(context.Background(), withSubagentAgent(), withSubagentProject(project.withSkills(nil)), WithModel(model), WithTool(selected[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer subagent.Close()
	run, err := subagent.Start(context.Background(), userRequest("subagent-tools"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	requests := model.Requests()
	if len(requests) < 1 || len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "search" {
		t.Fatalf("subagent provider tools = %#v", requests)
	}
	for _, tool := range requests[0].Tools {
		if isSubagentToolName(tool.Name) {
			t.Fatalf("subagent received mainAgent-only management tool %q", tool.Name)
		}
	}
	assertSubagentTextOnlyRequests(t, requests)
}

func assertSubagentTextOnlyRequests(t *testing.T, requests []agentruntime.ModelRequest) {
	t.Helper()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want one final-text request: %#v", len(requests), requests)
	}
	for _, request := range requests {
		for _, tool := range request.Tools {
			if tool.Name == "report_subagent_result" {
				t.Fatalf("retired completion tool is registered: %#v", request.Tools)
			}
		}
	}
}

func TestProjectModelForUsesDefinitionProviderAndModel(t *testing.T) {
	var requestModel string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requestModel = body.Model
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: [DONE]\\n\\n"))
	}))
	defer server.Close()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers:\n  primary:\n    type: openai\n    api_key: primary-key\n  subagent:\n    type: openai\n    url: "+server.URL+"\n    api_key: subagent-key\n    request_timeout: 1s\n")
	writeMainAgentDefinition(t, root, "primary", "primary-model", "")
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	model, err := project.modelFor("subagent", "subagent-selected")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := model.Start(context.Background(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Type: agentruntime.MessageTypeUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Subscribe(context.Background()) {
	}
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
	if requestModel != "subagent-selected" {
		t.Fatalf("model request = %q", requestModel)
	}
	if _, err := project.modelFor("missing", "subagent-selected"); err == nil {
		t.Fatal("unknown provider unexpectedly constructed a model")
	}
	if _, err := project.modelFor("subagent", ""); err == nil {
		t.Fatal("empty model unexpectedly constructed a model")
	}
}

func writeSubagentDefinition(t *testing.T, root, name, contents string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, ".agentcli", "agent", name, name+".md"), contents)
}
