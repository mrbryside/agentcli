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
		!strings.Contains(subagents, "## IMPORTANT: Tool-result protocol") ||
		!strings.Contains(subagents, "accepted=true means exactly one dispatch") ||
		!strings.Contains(subagents, "accepted=false with callback_action=automatic_existing") ||
		!strings.Contains(subagents, "Never retry with changed wording") ||
		!strings.Contains(subagents, "Only a later delivered result with completed, incomplete, or failed") ||
		!strings.Contains(subagents, "start_subagent always creates a new separately addressed child") {
		t.Fatalf("subagent tool-result protocol = %q", subagents)
	}
	if mainInstructions != "# Main agent instructions\n\nCoordinate work and communicate the outcome clearly." {
		t.Fatalf("MAIN.md system prompt = %q", mainInstructions)
	}
	if !strings.Contains(skills, "# Skills") || !strings.Contains(skills, "<available_skills>") || !strings.Contains(skills, "<name>testing-go</name>") {
		t.Fatalf("skill prompt does not contain skill catalog: %q", skills)
	}
	if !strings.Contains(subagents, "<available_subagents>") || !strings.Contains(subagents, "<name>researcher</name>") || !strings.Contains(subagents, "<model>gpt-review</model>") || !strings.Contains(subagents, "<skill>testing-go</skill>") || !strings.Contains(subagents, "<tool>search</tool>") {
		t.Fatalf("subagent catalog = %q", subagents)
	}
	if !strings.Contains(subagents, "discovery-only") || !strings.Contains(subagents, "do not start a child") {
		t.Fatalf("catalog does not protect discovery-only requests: %q", subagents)
	}
	if !strings.Contains(subagents, "default is to answer directly") || !strings.Contains(subagents, "do not trigger delegation by themselves") || !strings.Contains(subagents, "explicit requirement remains a valid trigger") || !strings.Contains(subagents, "Mere topic overlap") {
		t.Fatalf("catalog does not prevent unnecessary delegation: %q", subagents)
	}
	for _, expected := range []string{
		"<subagent_orchestration_rules>",
		"## Catalog and authority",
		"selection metadata",
		"only agent allowed",
		"Children never receive subagent-management tools",
		"Destructive child closure is application-owned",
		"## Trigger precedence and selection",
		"A subagent may be triggered in either of these ways",
		"1. Explicit requirement:",
		"This trigger takes precedence",
		"2. Description match:",
		"when no applicable instruction already requires a different delegation route",
		"Explicit requirements take precedence over description matching",
		"Before selecting a subagent by description",
		"use that required route first",
		"Never replace, bypass, or delay it",
		"follow normal instruction priority rather than description similarity",
		"After satisfying the explicit requirement",
		"select the single best matching definition",
		"## Instance selection and fan-out",
		"Prefer one child at a time",
		"ordinary lookup or research must start one child",
		"genuinely independent work",
		"always creates a new separately addressed child",
		"never reuses, resumes, or continues an existing child",
		"use send_subagent_message",
		"use start_subagent or send_subagent_message to check status",
		"result has been delivered and consumed",
		"continue_after_dispatch controls only whether the parent continues immediately",
		"It does not control subagent concurrency",
		"Before every start_subagent call",
		"required continue_after_dispatch value",
		"continue_after_dispatch=false",
		"no specific independent work to do before the subagent results are available",
		"subagents keep running",
		"runtime resumes the parent when a result is ready",
		"continue_after_dispatch=true",
		"commitment to perform only that work",
		"use the same value on every call",
		"Set every call to false",
		"Set every call to true",
		"Never mix values",
		"those subagents still run in parallel",
		"Start four subagents for four independent sources",
		"All four subagents still run in parallel while the parent stops",
		"Before every send_subagent_message call",
		"duplicate, already_sent, and callback_pending end the current successful tool batch automatically",
		"recovery_exhausted does not end the turn",
		"## Dispatch result handling",
		"Dispatch is not completion",
		"start_subagent and send_subagent_message are always asynchronous",
		"both require continue_after_dispatch",
		"false stops the parent after an accepted successful batch",
		"true returns control only for specific independent parent work",
		"Returning control does not require assistant content or another tool call",
		"accepted=true",
		"Post-dispatch turn policy",
		"already planned before dispatch",
		"outside the delegated task",
		"independent of every delegated result",
		"successful tool batch ends automatically",
		"stop and wait for the subagent results",
		"merely to simulate waiting",
		"call a response or delivery tool",
		"runtime resumes the parent automatically",
		"callback_action=automatic_existing",
		"recovery_exhausted",
		"retry the same dispatch with changed wording",
		"poll lifecycle state",
		"## Subagent result handling",
		"Delivered subagent results are authoritative",
		"Read callback_progress before deciding what to do",
		"pending_callbacks identifies accepted subagent dispatches",
		"received_callbacks identifies the callback turns already delivered",
		"process each exactly once",
		"independent of every outstanding result",
		"make no more tool calls or assistant content",
		"Do not call any tool to simulate waiting",
		"call a response or delivery tool",
		"combine the received outcomes once",
		"reopens this callback barrier",
		"## Follow-up and lifecycle",
		"send_subagent_message targets an idle incomplete or failed child only",
		"latest callback has been received and consumed",
		"incomplete children remain open",
		"at most one recovery dispatch per child",
		"automatically closes completed and failed children",
		"one-shot system reminder",
		"## Safety",
		"</subagent_orchestration_rules>",
	} {
		if !strings.Contains(subagents, expected) {
			t.Fatalf("catalog does not contain callback-orchestration rule %q: %q", expected, subagents)
		}
	}
	explicitRequirement := strings.Index(subagents, "1. Explicit requirement:")
	descriptionMatch := strings.Index(subagents, "2. Description match:")
	if explicitRequirement < 0 || descriptionMatch < 0 || explicitRequirement >= descriptionMatch {
		t.Fatalf("subagent discovery prompt does not prioritize explicit requirements before description matching: %q", subagents)
	}
	if strings.Contains(subagents, "close_subagent") {
		t.Fatalf("removed destructive tool still appears in the model system prompt: %q", subagents)
	}
	for _, contradictory := range []string{"WAITING_FOR_CALLBACK", "finish through the application's normal response", "use the application's waiting handoff"} {
		if strings.Contains(subagents, contradictory) {
			t.Fatalf("subagent prompt contains contradictory waiting instruction %q: %q", contradictory, subagents)
		}
	}
	if strings.Contains(subagents, "Use sources and explain uncertainty.") {
		t.Fatalf("definition instructions were eagerly exposed: %q", subagents)
	}
	if strings.Contains(strings.Join(prompts, "\n"), "Always explain failures clearly.") {
		t.Fatalf("AGENTS.md was included in main system prompts: %#v", prompts)
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
	joinedPrompts := strings.Join(prompts, "\n")
	if len(prompts) == 0 || !strings.Contains(joinedPrompts, "testing-go") || strings.Contains(joinedPrompts, "reviewing-go") {
		t.Fatalf("child skill prompt = %#v", prompts)
	}
	withoutSkills := project.withSkills(nil)
	if len(withoutSkills.Skills()) != 0 || strings.Contains(strings.Join(withoutSkills.SystemPrompts(), "\n"), "available_skills") {
		t.Fatalf("child without skills = %#v", withoutSkills.SystemPrompts())
	}
}

func TestChildSystemPromptsKeepAssignmentSeparateFromFramework(t *testing.T) {
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
		"# Sensitive information",
		modelSecretSafetyPrompt,
		"# Skills",
		"<name>testing-go</name>",
		"# Delivery contract",
		subagentCompletionPrompt,
	} {
		if !strings.Contains(framework, expected) {
			t.Fatalf("child framework prompt does not contain %q: %q", expected, framework)
		}
	}
	if strings.Contains(framework, "# Assignment role") || strings.Contains(framework, definition.Instructions) {
		t.Fatalf("child framework prompt contains subagent instructions: %q", framework)
	}
	if strings.Contains(framework, "<name>reviewing-go</name>") || strings.Contains(framework, "Run go test ./...") {
		t.Fatalf("child framework prompt exposes disallowed skill or eager skill body: %q", framework)
	}
	if strings.Contains(framework, "Coordinate work and communicate the outcome clearly.") {
		t.Fatalf("child prompt contains main-only instructions: %q", framework)
	}
	if configuration.systemPrompts[1] != "# Assignment role\n\n"+definition.Instructions {
		t.Fatalf("subagent instruction system prompt = %q", configuration.systemPrompts[1])
	}
	if strings.Contains(strings.Join(configuration.systemPrompts, "\n"), "Always explain failures clearly.") {
		t.Fatalf("AGENTS.md was included in child system prompts: %#v", configuration.systemPrompts)
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
		{name: "selected skill", skills: []string{"testing-go"}, wantTools: 2},
		{name: "no skills", wantTools: 1},
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
			if len(requests) < 1 || len(requests[0].Tools) != test.wantTools {
				t.Fatalf("provider tools = %#v", requests)
			}
			if len(requests[0].SystemPrompts) != 2 {
				t.Fatalf("provider system prompts = %#v", requests[0].SystemPrompts)
			}
			if requests[0].Tools[len(requests[0].Tools)-1].Name != toolexecution.SubagentOutcomeToolName {
				t.Fatalf("child outcome tool = %#v", requests[0].Tools)
			}
			if len(test.skills) != 0 && requests[0].Tools[0].Name != SkillLoaderToolName {
				t.Fatalf("provider tools = %#v", requests[0].Tools)
			}
			assertOutcomeRepairRequest(t, requests)
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
	if len(requests) < 1 || len(requests[0].Tools) != 2 || requests[0].Tools[0].Name != "search" || requests[0].Tools[1].Name != toolexecution.SubagentOutcomeToolName {
		t.Fatalf("child provider tools = %#v", requests)
	}
	for _, tool := range requests[0].Tools {
		if isSubagentToolName(tool.Name) {
			t.Fatalf("child received parent-only management tool %q", tool.Name)
		}
	}
	assertOutcomeRepairRequest(t, requests)
}

func assertOutcomeRepairRequest(t *testing.T, requests []agentruntime.ModelRequest) {
	t.Helper()
	if len(requests) != defaultCompletionRepairLimit+1 {
		t.Fatalf("provider requests = %d, want initial request and %d repairs: %#v", len(requests), defaultCompletionRepairLimit, requests)
	}
	for index, repair := range requests[1:] {
		if len(repair.Tools) != 1 || repair.Tools[0].Name != toolexecution.SubagentOutcomeToolName {
			t.Fatalf("repair %d tools = %#v, want only outcome tool", index+1, repair.Tools)
		}
		if !hasSubagentOutcomeRepairReminder(repair) {
			t.Fatalf("repair %d reminders = %#v", index+1, repair.ContextReminders)
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
	writeTestFile(t, filepath.Join(root, ".agentcli", "config.yaml"), "providers:\n  primary:\n    type: openai\n    api_key: primary-key\n  child:\n    type: openai\n    url: "+server.URL+"\n    api_key: child-key\n    request_timeout: 1s\n")
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
