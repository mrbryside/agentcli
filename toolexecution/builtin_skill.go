package toolexecution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/storage"
)

const (
	SkillLoaderToolName          = "load_skill"
	defaultSkillMaxTurnDistance  = 10
	defaultSkillMaxTokenDistance = 12_000
)

// Skill contains the provider-neutral content consumed by load_skill.
type Skill struct {
	Name         string
	Description  string
	Instructions string
}

// SkillReloadPolicy controls when an already-loaded skill is refreshed near
// the latest message history. A zero threshold disables that threshold.
type SkillReloadPolicy struct {
	MaxTurnDistance  int
	MaxTokenDistance int
}

func DefaultSkillReloadPolicy() SkillReloadPolicy {
	return SkillReloadPolicy{MaxTurnDistance: defaultSkillMaxTurnDistance, MaxTokenDistance: defaultSkillMaxTokenDistance}
}

func (policy SkillReloadPolicy) Validate() error {
	if policy.MaxTurnDistance < 0 {
		return errors.New("skill maximum turn distance cannot be negative")
	}
	if policy.MaxTokenDistance < 0 {
		return errors.New("skill maximum token distance cannot be negative")
	}
	return nil
}

type SkillLoader struct {
	skills   map[string]Skill
	messages storage.MessageStorage
	policy   SkillReloadPolicy

	mu           sync.Mutex
	reservations map[skillReservationKey]skillReservation
}

type skillReservationKey struct {
	sessionID string
	skillName string
}

type skillReservation struct {
	turnID      string
	contentHash string
}

// SkillToolResult is the JSON result domain emitted by load_skill.
type SkillToolResult struct {
	Status                  string `json:"status"`
	Name                    string `json:"name"`
	LoadTriggerSatisfiedFor string `json:"load_trigger_satisfied_for"`
	Description             string `json:"description,omitempty"`
	Instructions            string `json:"instructions,omitempty"`
	ContentHash             string `json:"content_hash"`
	Reason                  string `json:"reason,omitempty"`
	InstructionsInContext   bool   `json:"instructions_in_context,omitempty"`
	Message                 string `json:"message,omitempty"`
}

func NewSkillLoader(skills []Skill, messages storage.MessageStorage, policy SkillReloadPolicy) *SkillLoader {
	catalog := make(map[string]Skill, len(skills))
	for _, skill := range skills {
		catalog[skill.Name] = skill
	}
	return &SkillLoader{
		skills: catalog, messages: messages, policy: policy,
		reservations: make(map[skillReservationKey]skillReservation),
	}
}

// Tool returns the framework-owned load_skill built-in.
func (loader *SkillLoader) Tool() Tool {
	return Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        SkillLoaderToolName,
			Description: "Load one skill's full instructions after a valid load trigger for that specific skill. Each call loads only the exact skill named in the request; it never loads skills collectively. Valid triggers are: (1) a skill description in available_skills directly matches the task and you are about to apply that skill; (2) another applicable instruction explicitly requires that skill or requires a skill for the selected workflow; or (3) the user asks to inspect the skill's full instructions. An explicit requirement is mandatory before the action or answer it governs. A successful load from an earlier turn does not satisfy a new load trigger. A tool result and the provider steps that follow it continue the current turn and do not create another load trigger by themselves. When a separate valid trigger applies in a later user-message or callback turn, call this tool instead of inferring freshness from instructions visible in earlier turns. Skill caching and freshness are runtime-managed. Tool, subagent, and other capability descriptions may help selection but never authorize bypassing a required skill load. Discovery-only questions about available skills, their descriptions, or which skill might fit do not trigger this tool unless another applicable instruction explicitly requires loading one. Never load an irrelevant skill as a substitute for a missing capability or tool. Inspect the complete result. Every successful result uses status=loaded and means the load request succeeded for that exact named skill. The result's name and load_trigger_satisfied_for identify the one exact skill that loaded and satisfies only the current load trigger for that named skill. Do not call load_skill again for that same trigger merely because a tool returned or another provider step began. This does not block loading the same skill for a separate valid trigger in a later user-message or callback turn. It does not load or satisfy a trigger for any other skill; load another skill only when that other skill has a separate valid trigger. When instructions_in_context=true, the named skill's full instructions are already available in the conversation context even though the result does not repeat them. This tool only makes the named skill's instructions available and does not decide whether the turn should continue, wait, or end.",
			InputSchema: mustRawToolSchema(`{"type":"object","properties":{"name":{"type":"string","minLength":1,"description":"Exact skill name selected from available_skills after a valid description-match, explicit-requirement, or explicit-inspection trigger."}},"required":["name"],"additionalProperties":false}`),
		},
		Handler: loader.handle,
	}
}

func (loader *SkillLoader) handle(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, fmt.Errorf("decode skill request: %w", err)
	}
	skill, found := loader.skills[input.Name]
	if !found {
		return nil, fmt.Errorf("skill %q is not available", input.Name)
	}
	invocation, ok := InvocationFromContext(ctx)
	if !ok || invocation.ToolName != SkillLoaderToolName {
		return nil, errors.New("load_skill requires tool invocation context")
	}

	hash := hashSkill(skill)
	key := skillReservationKey{sessionID: invocation.SessionID, skillName: skill.Name}
	loader.mu.Lock()
	reservation := loader.reservations[key]
	if reservation.turnID == invocation.TurnID && reservation.contentHash == hash {
		loader.mu.Unlock()
		return marshalSkillFromContext(skill, hash, "instructions are already present in the conversation context for this turn")
	}
	loader.mu.Unlock()

	messages, err := loader.messages.List(ctx, invocation.SessionID)
	if err != nil {
		return nil, fmt.Errorf("inspect skill history: %w", err)
	}
	previous, index, found := latestSkillLoad(messages, skill.Name)
	reason := "first load"
	reload := !found
	if found {
		switch {
		case previous.ContentHash != hash:
			reload, reason = true, "skill content changed"
		case loader.policy.MaxTurnDistance > 0 && turnDistance(messages, index) >= loader.policy.MaxTurnDistance:
			reload, reason = true, "prior instructions are old by turn distance"
		case loader.policy.MaxTokenDistance > 0 && tokenDistance(messages, index) >= loader.policy.MaxTokenDistance:
			reload, reason = true, "prior instructions are old by token distance"
		default:
			reason = "instructions are still recent in conversation history"
		}
	}
	if !reload {
		return marshalSkillFromContext(skill, hash, reason)
	}

	loader.mu.Lock()
	reservation = loader.reservations[key]
	if reservation.turnID == invocation.TurnID && reservation.contentHash == hash {
		loader.mu.Unlock()
		return marshalSkillFromContext(skill, hash, "instructions are already present in the conversation context for this turn")
	}
	loader.reservations[key] = skillReservation{turnID: invocation.TurnID, contentHash: hash}
	loader.mu.Unlock()

	return json.Marshal(SkillToolResult{
		Status: "loaded", Name: skill.Name, LoadTriggerSatisfiedFor: skill.Name, Description: skill.Description,
		Instructions: skill.Instructions, ContentHash: hash, Reason: reason,
		Message: skillLoadedMessage(skill.Name, false),
	})
}

func marshalSkillFromContext(skill Skill, hash, reason string) (json.RawMessage, error) {
	return json.Marshal(SkillToolResult{
		Status: "loaded", Name: skill.Name, LoadTriggerSatisfiedFor: skill.Name, ContentHash: hash, Reason: reason,
		InstructionsInContext: true,
		Message:               skillLoadedMessage(skill.Name, true),
	})
}

func skillLoadedMessage(name string, instructionsInContext bool) string {
	if instructionsInContext {
		return fmt.Sprintf("The requested skill %q loaded successfully. The current load trigger for %q is satisfied. Its full instructions are already available in the conversation context.", name, name)
	}
	return fmt.Sprintf("The requested skill %q loaded successfully. The current load trigger for %q is satisfied. Its full instructions are included in this result.", name, name)
}

func hashSkill(skill Skill) string {
	digest := sha256.Sum256([]byte(skill.Name + "\x00" + skill.Description + "\x00" + skill.Instructions))
	return hex.EncodeToString(digest[:])
}

func latestSkillLoad(messages []storage.Message, name string) (SkillToolResult, int, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Type != storage.MessageTypeToolResult || message.ToolResult == nil ||
			message.ToolResult.Name != SkillLoaderToolName || message.ToolResult.Status != storage.ToolResultSucceeded {
			continue
		}
		var result SkillToolResult
		if json.Unmarshal(message.ToolResult.Output, &result) != nil || result.Name != name || result.Instructions == "" {
			continue
		}
		if result.ContentHash == "" {
			result.ContentHash = hashSkill(Skill{Name: result.Name, Description: result.Description, Instructions: result.Instructions})
		}
		return result, index, true
	}
	return SkillToolResult{}, -1, false
}

func turnDistance(messages []storage.Message, loadedIndex int) int {
	loadedTurn := messages[loadedIndex].TurnID
	turns := make(map[string]struct{})
	for _, message := range messages[loadedIndex+1:] {
		if message.TurnID != "" && message.TurnID != loadedTurn {
			turns[message.TurnID] = struct{}{}
		}
	}
	return len(turns)
}

func tokenDistance(messages []storage.Message, loadedIndex int) int {
	bytes := 0
	for _, message := range messages[loadedIndex+1:] {
		encoded, err := json.Marshal(message)
		if err == nil {
			bytes += len(encoded)
		}
	}
	return (bytes + 3) / 4
}
