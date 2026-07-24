package agentcli

import (
	"context"
	"strings"
	"testing"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/permission"
)

func TestGuardProviderOptionsValidateTheirPairing(t *testing.T) {
	tests := []struct {
		name    string
		options []Option
		want    string
	}{
		{
			name:    "input provider requires prompt",
			options: []Option{WithModel(&scriptedModel{}), WithInputGuardProvider("guard", "model")},
			want:    "input guard provider requires input guard prompt",
		},
		{
			name:    "output provider requires prompt",
			options: []Option{WithModel(&scriptedModel{}), WithOutputGuardProvider("guard", "model")},
			want:    "output guard provider requires output guard prompt",
		},
		{
			name:    "input provider requires project",
			options: []Option{WithModel(&scriptedModel{}), WithInputGuardPrompt("check input"), WithInputGuardProvider("guard", "model")},
			want:    "guard provider requires a project",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(context.Background(), test.options...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestGuardPromptOptionsRejectWhitespace(t *testing.T) {
	tests := []struct {
		name   string
		option Option
		want   string
	}{
		{name: "input", option: WithInputGuardPrompt(" \n\t"), want: "input guard prompt is required"},
		{name: "output", option: WithOutputGuardPrompt(" \n\t"), want: "output guard prompt is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(context.Background(), WithModel(&scriptedModel{}), test.option)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestGuardProviderOptionsCannotBeCombinedWithFunctionGuard(t *testing.T) {
	inputGuard := func(context.Context, agentruntime.InputGuardAttempt) (agentruntime.InputGuardDecision, error) {
		return agentruntime.InputGuardDecision{Action: agentruntime.InputAccept}, nil
	}
	_, err := New(context.Background(),
		WithModel(&scriptedModel{}),
		WithInputGuard(inputGuard),
		WithInputGuardPrompt("check input"),
		WithInputGuardProvider("guard", "model"),
	)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("New() error = %v, want function/prompt guard conflict", err)
	}
}

func TestToolGuardProviderRequiresProject(t *testing.T) {
	tool := testTool("guarded")
	tool.ToolOutputGuardPrompt = "allow valid output"
	tool.ToolOutputGuardModel = &GuardModelConfig{Provider: "policy", Model: "guard-small"}
	_, err := New(context.Background(),
		WithModel(&scriptedModel{}),
		WithTool(tool),
	)
	if err == nil || !strings.Contains(err.Error(), "tool-output guard provider requires a project") {
		t.Fatalf("New() error = %v, want missing project", err)
	}
}

func TestToolGuardModelConfigResolvesProjectProvider(t *testing.T) {
	project := &Project{
		root:         t.TempDir(),
		providerName: "primary",
		modelName:    "main-model",
		config: ProjectConfig{
			PermissionMode: permission.Default,
			Providers: map[string]ProviderConfig{
				"primary": {Type: ProviderTypeOpenAI, APIKey: "primary-key"},
				"policy":  {Type: ProviderTypeOpenAI, APIKey: "policy-key"},
			},
		},
	}
	tool := testTool("guarded")
	tool.ToolOutputGuardPrompt = "allow valid output"
	tool.ToolOutputGuardModel = &GuardModelConfig{Provider: "policy", Model: "guard-small"}
	agent, err := New(context.Background(),
		WithProject(project),
		WithModel(&scriptedModel{}),
		WithTool(tool),
	)
	if err != nil {
		t.Fatalf("New() error = %v, want configured policy provider", err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}

	tool.ToolOutputGuardModel = &GuardModelConfig{Provider: "missing", Model: "guard-small"}
	_, err = New(context.Background(),
		WithProject(project),
		WithModel(&scriptedModel{}),
		WithTool(tool),
	)
	if err == nil || !strings.Contains(err.Error(), `provider "missing" is not configured`) {
		t.Fatalf("New() error = %v, want unknown provider validation", err)
	}
}
