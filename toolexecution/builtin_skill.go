package toolexecution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"harness-api/agentruntime"
	"harness-api/storage"
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
	Status                 string `json:"status"`
	Name                   string `json:"name"`
	Description            string `json:"description,omitempty"`
	Instructions           string `json:"instructions,omitempty"`
	ContentHash            string `json:"content_hash"`
	Reason                 string `json:"reason,omitempty"`
	DoNotCallAgainThisTurn bool   `json:"do_not_call_again_this_turn,omitempty"`
	NextAction             string `json:"next_action,omitempty"`
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
			Description: "Load full instructions only when applying a relevant selected skill or explicitly inspecting its instructions. Do not call this tool to list available skills or for other discovery-only requests. Never use a skill as a substitute for a missing capability or tool. If it returns already_loaded, continue the task and never call load_skill again in the same turn.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Exact skill name from available_skills"}},"required":["name"],"additionalProperties":false}`),
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
		return marshalSkillAlreadyLoaded(skill, hash, "already loaded in this turn")
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
		return marshalSkillAlreadyLoaded(skill, hash, reason)
	}

	loader.mu.Lock()
	reservation = loader.reservations[key]
	if reservation.turnID == invocation.TurnID && reservation.contentHash == hash {
		loader.mu.Unlock()
		return marshalSkillAlreadyLoaded(skill, hash, "already loaded in this turn")
	}
	loader.reservations[key] = skillReservation{turnID: invocation.TurnID, contentHash: hash}
	loader.mu.Unlock()

	return json.Marshal(SkillToolResult{
		Status: "loaded", Name: skill.Name, Description: skill.Description,
		Instructions: skill.Instructions, ContentHash: hash, Reason: reason,
	})
}

func marshalSkillAlreadyLoaded(skill Skill, hash, reason string) (json.RawMessage, error) {
	return json.Marshal(SkillToolResult{
		Status: "already_loaded", Name: skill.Name, ContentHash: hash, Reason: reason,
		DoNotCallAgainThisTurn: true,
		NextAction:             "Continue the task using the instructions already present in conversation history. If a required capability or tool is unavailable, report that limitation instead of calling load_skill again.",
	})
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
