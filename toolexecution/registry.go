// Package toolexecution registers provider-neutral tools and executes them.
package toolexecution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
)

// Handler executes a tool call with its JSON arguments.
type Handler func(context.Context, json.RawMessage) (json.RawMessage, error)

// GuardModelConfig selects one project provider profile and model for a
// prompt-backed guard. A nil configuration uses the Agent's main model.
type GuardModelConfig struct {
	Provider string
	Model    string
}

// TurnBehavior controls whether the runtime asks the model to continue after
// a successful tool result. ContinueTurn is the backwards-compatible default.
type TurnBehavior = agentruntime.ToolTurnBehavior

const (
	ContinueTurn TurnBehavior = agentruntime.ToolTurnContinue
	EndTurn      TurnBehavior = agentruntime.ToolTurnEnd
)

// Tool combines a provider-neutral definition with its implementation.
// Permission and PermissionWithPolicy control authorization. Confirmation is
// an independent, optional Yes/No user gate that is unaffected by permission
// policy or mode. RequiredAtTurnEnd asks agentcli's completion guard to require
// one successful invocation in every turn where the tool is exposed.
type Tool struct {
	Definition           agentruntime.ToolDefinition
	Handler              Handler
	TurnBehavior         TurnBehavior
	RequiredAtTurnEnd    bool
	ToolCallGuard        agentruntime.ToolCallGuard
	ToolCallGuardPrompt  string
	ToolCallGuardModel   *GuardModelConfig
	Permission           PermissionDescriptor
	PermissionWithPolicy PermissionPolicyDescriptor
	Confirmation         ConfirmationDescriptor
	resultTurnBehavior   func(json.RawMessage, json.RawMessage) TurnBehavior
}

// PermissionDescriptor describes the capabilities required by one invocation.
// StaticPermission is the convenient choice when every invocation has the same
// actions and risk.
type PermissionDescriptor func(json.RawMessage) (permission.Description, error)

// PermissionPolicyDescriptor is an optional admission callback that receives
// the immutable policy snapshot captured when a request enters the executor.
// Tool.Permission remains supported for custom tools that do not need policy
// dependent classification.
type PermissionPolicyDescriptor func(json.RawMessage, permission.Policy) (permission.Description, error)

// ConfirmationDescriptor builds the user-facing information for one Yes/No
// confirmation request. The handler runs only after a correlated Yes answer.
type ConfirmationDescriptor func(json.RawMessage) (confirmation.Description, error)

// Registry is a synchronized, ordered catalog of callable tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]registeredTool
	order []string
}

type registeredTool struct {
	definition            agentruntime.ToolDefinition
	handler               Handler
	turnBehavior          TurnBehavior
	toolCallGuard         agentruntime.ToolCallGuard
	toolCallGuardPrompt   string
	toolCallGuardProvider string
	toolCallGuardModel    string
	permission            PermissionDescriptor
	permissionWithPolicy  PermissionPolicyDescriptor
	confirmation          ConfirmationDescriptor
	resultTurnBehavior    func(json.RawMessage, json.RawMessage) TurnBehavior
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]registeredTool)}
}

// Register adds tool to the catalog. Tool names are unique and schemas must be
// valid JSON Schema objects (with type set to object).
func (r *Registry) Register(tool Tool) error {
	if tool.Definition.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if tool.Handler == nil {
		return fmt.Errorf("tool %q handler is required", tool.Definition.Name)
	}
	if tool.TurnBehavior != ContinueTurn && tool.TurnBehavior != EndTurn {
		return fmt.Errorf("tool %q has unsupported turn behavior %q", tool.Definition.Name, tool.TurnBehavior)
	}
	if tool.RequiredAtTurnEnd && tool.TurnBehavior != EndTurn {
		return fmt.Errorf("tool %q required at turn end must use end turn behavior", tool.Definition.Name)
	}
	rawGuardPrompt := tool.ToolCallGuardPrompt
	tool.ToolCallGuardPrompt = strings.TrimSpace(rawGuardPrompt)
	if rawGuardPrompt != "" && tool.ToolCallGuardPrompt == "" {
		return fmt.Errorf("tool %q tool-call guard prompt is empty", tool.Definition.Name)
	}
	if tool.ToolCallGuard != nil && tool.ToolCallGuardPrompt != "" {
		return fmt.Errorf("tool %q cannot configure both a tool-call guard and prompt", tool.Definition.Name)
	}
	var guardProvider, guardModel string
	if tool.ToolCallGuardModel != nil {
		guardProvider = strings.TrimSpace(tool.ToolCallGuardModel.Provider)
		guardModel = strings.TrimSpace(tool.ToolCallGuardModel.Model)
		if guardProvider == "" {
			return fmt.Errorf("tool %q tool-call guard model provider is required", tool.Definition.Name)
		}
		if guardModel == "" {
			return fmt.Errorf("tool %q tool-call guard model name is required", tool.Definition.Name)
		}
		if tool.ToolCallGuardPrompt == "" {
			return fmt.Errorf("tool %q tool-call guard model requires a prompt guard", tool.Definition.Name)
		}
	}
	if err := validateInputSchema(tool.Definition.InputSchema); err != nil {
		return fmt.Errorf("tool %q input schema: %w", tool.Definition.Name, err)
	}

	definition := cloneDefinition(tool.Definition)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[definition.Name]; exists {
		return fmt.Errorf("tool %q is already registered", definition.Name)
	}
	r.tools[definition.Name] = registeredTool{
		definition: definition, handler: tool.Handler, turnBehavior: tool.TurnBehavior,
		toolCallGuard: tool.ToolCallGuard, toolCallGuardPrompt: tool.ToolCallGuardPrompt,
		toolCallGuardProvider: guardProvider, toolCallGuardModel: guardModel,
		permission: tool.Permission, permissionWithPolicy: tool.PermissionWithPolicy,
		confirmation: tool.Confirmation, resultTurnBehavior: tool.resultTurnBehavior,
	}
	r.order = append(r.order, definition.Name)
	return nil
}

func (r *Registry) confirmationFor(name string, arguments json.RawMessage) (confirmation.Description, error, bool) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return confirmation.Description{}, nil, false
	}
	if tool.confirmation == nil {
		return confirmation.Description{}, nil, true
	}
	description, err := tool.confirmation(cloneRawJSON(arguments))
	return description, err, true
}

// Definitions returns registered definitions in stable registration order.
func (r *Registry) Definitions() []agentruntime.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	definitions := make([]agentruntime.ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		definitions = append(definitions, cloneDefinition(r.tools[name].definition))
	}
	return definitions
}

// lookup retrieves a registered handler by name.
func (r *Registry) lookup(name string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool.handler, ok
}

func (r *Registry) callGuardFor(name string) (agentruntime.ToolCallGuard, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	if !ok {
		return nil, "", false
	}
	return tool.toolCallGuard, tool.toolCallGuardPrompt, true
}

type promptCallGuardConfig struct {
	toolName     string
	providerName string
	modelName    string
}

func (r *Registry) promptCallGuards() []promptCallGuardConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	guards := make([]promptCallGuardConfig, 0)
	for _, name := range r.order {
		tool := r.tools[name]
		if tool.toolCallGuardPrompt != "" {
			guards = append(guards, promptCallGuardConfig{
				toolName:     name,
				providerName: tool.toolCallGuardProvider,
				modelName:    tool.toolCallGuardModel,
			})
		}
	}
	return guards
}

func (r *Registry) turnBehaviorFor(name string, arguments, output json.RawMessage) (TurnBehavior, bool) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return ContinueTurn, false
	}
	if tool.resultTurnBehavior != nil {
		return tool.resultTurnBehavior(cloneRawJSON(arguments), cloneRawJSON(output)), true
	}
	return tool.turnBehavior, true
}

func (r *Registry) permissionFor(name string, arguments json.RawMessage, policy permission.Policy) (permission.Description, error, bool) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return permission.Description{}, nil, false
	}
	if tool.permissionWithPolicy != nil {
		description, err := tool.permissionWithPolicy(cloneRawJSON(arguments), clonePolicyValue(policy))
		return description, err, true
	}
	if tool.permission == nil {
		return permission.Description{}, nil, true
	}
	description, err := tool.permission(cloneRawJSON(arguments))
	return description, err, true
}

func validateInputSchema(schema agentruntime.ToolSchema) error {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("must be valid JSON Schema: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return fmt.Errorf("must be valid JSON: %w", err)
	}
	if object == nil {
		return fmt.Errorf("must be a JSON object")
	}

	rawType, ok := object["type"]
	if !ok {
		return fmt.Errorf("must declare type object")
	}
	var schemaType string
	if err := json.Unmarshal(rawType, &schemaType); err != nil || schemaType != "object" {
		return fmt.Errorf("type must be object")
	}
	return nil
}

func cloneDefinition(definition agentruntime.ToolDefinition) agentruntime.ToolDefinition {
	clone := definition
	clone.InputSchema = definition.InputSchema.Clone()
	return clone
}

func mustRawToolSchema(raw string) agentruntime.ToolSchema {
	schema, err := agentruntime.RawToolSchema(json.RawMessage(raw))
	if err != nil {
		panic(fmt.Sprintf("invalid framework tool schema: %v", err))
	}
	return schema
}
