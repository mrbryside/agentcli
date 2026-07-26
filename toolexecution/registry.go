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

// Tool combines a provider-neutral definition with its implementation.
// Permission and PermissionWithPolicy control authorization. Confirmation is
// an independent, optional Yes/No user gate that is unaffected by permission
// policy or mode. Trigger selects required execution timing and handler
// delivery. EndTurnOnSuccess independently controls whether a successful batch
// containing the tool ends the current turn.
type Tool struct {
	Definition       agentruntime.ToolDefinition
	Handler          Handler
	Trigger          ToolTrigger
	EndTurnOnSuccess bool
	// CanonicalAssistantMessageParameter names a required string argument whose
	// value becomes the durable assistant message after an EndResponseScope
	// handler succeeds. Failed or cancelled delivery never records it.
	CanonicalAssistantMessageParameter string
	ToolCallGuard                      agentruntime.ToolCallGuard
	ToolCallGuardPrompt                string
	ToolCallGuardModel                 *GuardModelConfig
	Permission                         PermissionDescriptor
	PermissionWithPolicy               PermissionPolicyDescriptor
	Confirmation                       ConfirmationDescriptor
	resultTurnBehavior                 func(json.RawMessage, json.RawMessage) agentruntime.ToolTurnBehavior
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
	definition                         agentruntime.ToolDefinition
	handler                            Handler
	trigger                            ToolTrigger
	turnBehavior                       agentruntime.ToolTurnBehavior
	toolCallGuard                      agentruntime.ToolCallGuard
	toolCallGuardPrompt                string
	toolCallGuardProvider              string
	toolCallGuardModel                 string
	permission                         PermissionDescriptor
	permissionWithPolicy               PermissionPolicyDescriptor
	confirmation                       ConfirmationDescriptor
	canonicalAssistantMessageParameter string
	resultTurnBehavior                 func(json.RawMessage, json.RawMessage) agentruntime.ToolTurnBehavior
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
	if tool.Trigger != "" && tool.Trigger != EndTurn && tool.Trigger != EndResponseScope {
		return fmt.Errorf("tool %q has unsupported trigger %q", tool.Definition.Name, tool.Trigger)
	}
	tool.CanonicalAssistantMessageParameter = strings.TrimSpace(tool.CanonicalAssistantMessageParameter)
	if tool.CanonicalAssistantMessageParameter != "" {
		if tool.Trigger != EndResponseScope {
			return fmt.Errorf("tool %q canonical assistant message requires the end-response-scope trigger", tool.Definition.Name)
		}
		if err := validateCanonicalAssistantParameter(tool.Definition.InputSchema, tool.CanonicalAssistantMessageParameter); err != nil {
			return fmt.Errorf("tool %q canonical assistant message parameter: %w", tool.Definition.Name, err)
		}
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
	turnBehavior := agentruntime.ToolTurnContinue
	if tool.EndTurnOnSuccess {
		turnBehavior = agentruntime.ToolTurnEndOnSuccess
	}
	r.tools[definition.Name] = registeredTool{
		definition: definition, handler: tool.Handler, trigger: tool.Trigger, turnBehavior: turnBehavior,
		toolCallGuard: tool.ToolCallGuard, toolCallGuardPrompt: tool.ToolCallGuardPrompt,
		toolCallGuardProvider: guardProvider, toolCallGuardModel: guardModel,
		permission: tool.Permission, permissionWithPolicy: tool.PermissionWithPolicy,
		confirmation:                       tool.Confirmation,
		canonicalAssistantMessageParameter: tool.CanonicalAssistantMessageParameter,
		resultTurnBehavior:                 tool.resultTurnBehavior,
	}
	r.order = append(r.order, definition.Name)
	return nil
}

func (r *Registry) triggerFor(name string) (ToolTrigger, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool.trigger, ok
}

func (r *Registry) hasEndResponseScopeTools() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, tool := range r.tools {
		if tool.trigger == EndResponseScope {
			return true
		}
	}
	return false
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

func (r *Registry) turnBehaviorFor(name string, arguments, output json.RawMessage) (agentruntime.ToolTurnBehavior, bool) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return agentruntime.ToolTurnContinue, false
	}
	if tool.resultTurnBehavior != nil {
		return tool.resultTurnBehavior(cloneRawJSON(arguments), cloneRawJSON(output)), true
	}
	return tool.turnBehavior, true
}

func (r *Registry) canonicalAssistantMessageParameterFor(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name].canonicalAssistantMessageParameter
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

func validateCanonicalAssistantParameter(schema agentruntime.ToolSchema, name string) error {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var object struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(encoded, &object); err != nil {
		return err
	}
	property, exists := object.Properties[name]
	if !exists {
		return fmt.Errorf("%q is not declared in the input schema", name)
	}
	if property.Type != "string" {
		return fmt.Errorf("%q must be a string property", name)
	}
	for _, required := range object.Required {
		if required == name {
			return nil
		}
	}
	return fmt.Errorf("%q must be required", name)
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
