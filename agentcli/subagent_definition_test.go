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
	"testing"

	"harness-api/agentruntime"
	"harness-api/toolexecution"
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
	definitions := project.Subagents()
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

	prompts := project.SystemPrompts()
	if len(prompts) != 2 {
		t.Fatalf("system prompts = %#v", prompts)
	}
	catalog := prompts[0]
	if !strings.Contains(catalog, "<available_skills>") || !strings.Contains(catalog, "<name>testing-go</name>") {
		t.Fatalf("main prompt does not contain skill catalog: %q", catalog)
	}
	if !strings.Contains(catalog, "<available_subagents>") || !strings.Contains(catalog, "<name>researcher</name>") || !strings.Contains(catalog, "<model>gpt-review</model>") || !strings.Contains(catalog, "<skill>testing-go</skill>") || !strings.Contains(catalog, "<tool>search</tool>") {
		t.Fatalf("subagent catalog = %q", catalog)
	}
	if !strings.Contains(catalog, "discovery-only") || !strings.Contains(catalog, "do not start a child") {
		t.Fatalf("catalog does not protect discovery-only requests: %q", catalog)
	}
	if !strings.Contains(catalog, "default is to answer the user directly") || !strings.Contains(catalog, "Do not delegate simple answers") || !strings.Contains(catalog, "Mere topic overlap") {
		t.Fatalf("catalog does not prevent unnecessary delegation: %q", catalog)
	}
	for _, expected := range []string{"only agent allowed", "children never receive subagent-management tools", "Never poll while waiting", "wait passively", "send_subagent_message", "another callback will automatically resume you", "display_name", "exactly one child is open", "ask the user which display_name", "new_instance=true", "bounded one-shot work", "concrete planned follow-up", "mere possibility"} {
		if !strings.Contains(catalog, expected) {
			t.Fatalf("catalog does not contain callback-orchestration rule %q: %q", expected, catalog)
		}
	}
	if strings.Contains(catalog, "Use sources and explain uncertainty.") {
		t.Fatalf("definition instructions were eagerly exposed: %q", catalog)
	}
	if prompts[1] != project.agents {
		t.Fatalf("AGENTS system prompt = %q", prompts[1])
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
	child := project.withSkills([]string{"testing-go"})
	if skills := child.Skills(); len(skills) != 1 || skills[0].Name != "testing-go" {
		t.Fatalf("child skills = %#v", skills)
	}
	prompts := child.SystemPrompts()
	if len(prompts) == 0 || !strings.Contains(prompts[0], "testing-go") || strings.Contains(prompts[0], "reviewing-go") {
		t.Fatalf("child skill prompt = %#v", prompts)
	}
	withoutSkills := project.withSkills(nil)
	if len(withoutSkills.Skills()) != 0 || strings.Contains(strings.Join(withoutSkills.SystemPrompts(), "\n"), "available_skills") {
		t.Fatalf("child without skills = %#v", withoutSkills.SystemPrompts())
	}
}

func TestChildSystemPromptsGroupFrameworkMaterialAndKeepAgentsSeparate(t *testing.T) {
	project, err := LoadProject(projectFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	childProject := project.withSkills([]string{"testing-go"})
	definition := SubagentDefinition{
		Name:         "researcher",
		Provider:     "openai",
		Model:        "gpt-research",
		Instructions: "Investigate the delegated question and report concrete findings.",
	}
	configuration := defaultConfig(project.root)
	if err := withChildSystemPrompts(childProject, definition)(&configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.systemPrompts) != 2 {
		t.Fatalf("child system prompts = %#v", configuration.systemPrompts)
	}
	combined := configuration.systemPrompts[0]
	for _, expected := range []string{
		`configured "researcher" subagent`,
		"# Assignment role",
		definition.Instructions,
		"# Runtime context",
		`agent: "researcher"`,
		`provider: "openai"`,
		`model: "gpt-research"`,
		`working_directory: "` + project.root + `"`,
		"# Evidence and tool use",
		subagentCapabilityBoundaryPrompt,
		"# Sensitive information",
		modelSecretSafetyPrompt,
		"# Skills",
		"<name>testing-go</name>",
		"# Delivery contract",
		subagentCompletionPrompt,
	} {
		if !strings.Contains(combined, expected) {
			t.Fatalf("combined child prompt does not contain %q: %q", expected, combined)
		}
	}
	if strings.Contains(combined, "<name>reviewing-go</name>") || strings.Contains(combined, "Run go test ./...") {
		t.Fatalf("combined child prompt exposes disallowed skill or eager skill body: %q", combined)
	}
	if strings.Contains(combined, "Coordinate work and communicate the outcome clearly.") {
		t.Fatalf("child prompt contains main-only instructions: %q", combined)
	}
	if configuration.systemPrompts[1] != project.agents {
		t.Fatalf("AGENTS system prompt = %q", configuration.systemPrompts[1])
	}

	withoutAgents := *childProject
	withoutAgents.agents = ""
	configuration = defaultConfig(project.root)
	if err := withChildSystemPrompts(&withoutAgents, definition)(&configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.systemPrompts) != 1 {
		t.Fatalf("child prompts without AGENTS.md = %#v", configuration.systemPrompts)
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
			childProject := project.withSkills(test.skills)
			agent, err := New(context.Background(),
				withChildAgent(),
				withChildProject(childProject),
				withChildSystemPrompts(childProject, SubagentDefinition{Name: "researcher", Instructions: "Research the delegated task."}),
				WithModel(model),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()
			run, err := agent.Start(context.Background(), userRequest("child-skills"))
			if err != nil {
				t.Fatal(err)
			}
			waitRun(t, run)
			requests := model.Requests()
			if len(requests) != 1 || len(requests[0].Tools) != test.wantTools {
				t.Fatalf("provider tools = %#v", requests)
			}
			if len(requests[0].SystemPrompts) != 2 {
				t.Fatalf("provider system prompts = %#v", requests[0].SystemPrompts)
			}
			if test.wantTools == 1 && requests[0].Tools[0].Name != SkillLoaderToolName {
				t.Fatalf("provider tools = %#v", requests[0].Tools)
			}
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

	definition := project.Subagents()[0]
	selected := filterSubagentTools(definition, []toolexecution.Tool{testTool("write"), testTool("search")})
	if len(selected) != 1 || selected[0].Definition.Name != "search" {
		t.Fatalf("selected tools = %#v", selected)
	}
	model := &scriptedModel{}
	child, err := New(context.Background(), withChildAgent(), withChildProject(project.withSkills(nil)), WithModel(model), WithTool(selected[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	run, err := child.Start(context.Background(), userRequest("child-tools"))
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, run)
	requests := model.Requests()
	if len(requests) != 1 || len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "search" {
		t.Fatalf("child provider tools = %#v", requests)
	}
	for _, tool := range requests[0].Tools {
		if isSubagentToolName(tool.Name) {
			t.Fatalf("child received parent-only management tool %q", tool.Name)
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
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers:\n  primary:\n    api_key: primary-key\n  child:\n    url: "+server.URL+"\n    api_key: child-key\n    request_timeout: 1s\n")
	writeMainAgentDefinition(t, root, "primary", "primary-model", "")
	project, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	model, err := project.ModelFor("child", "child-selected")
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
	if requestModel != "child-selected" {
		t.Fatalf("model request = %q", requestModel)
	}
	if _, err := project.ModelFor("missing", "child-selected"); err == nil {
		t.Fatal("unknown provider unexpectedly constructed a model")
	}
	if _, err := project.ModelFor("child", ""); err == nil {
		t.Fatal("empty model unexpectedly constructed a model")
	}
}

func writeSubagentDefinition(t *testing.T, root, name, contents string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, ".agentcli", "agent", name, name+".md"), contents)
}
