package agentcli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/provider"
	"github.com/mrbryside/agentcli/storage"

	"github.com/chzyer/readline"
)

const terminalInterruptInput = "\x03"

// TerminalOption configures Agent.RunTerminal without changing the Agent's
// runtime, tools, storage, or lifecycle.
type TerminalOption func(*terminalConfig) error

type terminalConfig struct {
	input         io.Reader
	output        io.Writer
	initialPrompt string
	sessionID     string
}

// WithTerminalInput replaces stdin. It is useful for embedding and tests.
func WithTerminalInput(input io.Reader) TerminalOption {
	return func(config *terminalConfig) error {
		if input == nil {
			return errors.New("terminal input is required")
		}
		config.input = input
		return nil
	}
}

// WithTerminalOutput replaces stdout. It is useful for embedding and tests.
func WithTerminalOutput(output io.Writer) TerminalOption {
	return func(config *terminalConfig) error {
		if output == nil {
			return errors.New("terminal output is required")
		}
		config.output = output
		return nil
	}
}

// WithTerminalInitialPrompt runs one prompt without opening the interactive
// input loop. Construct the Agent with WithNonInteractive(true) when tools
// must not pause for permission or confirmation input in this mode.
func WithTerminalInitialPrompt(prompt string) TerminalOption {
	return func(config *terminalConfig) error {
		config.initialPrompt = strings.TrimSpace(prompt)
		return nil
	}
}

// WithTerminalSessionID selects the session used by the terminal. Supplying a
// stable ID lets the caller inspect or continue the same transcript afterward.
func WithTerminalSessionID(sessionID string) TerminalOption {
	return func(config *terminalConfig) error {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return errors.New("terminal session ID is required")
		}
		config.sessionID = sessionID
		return nil
	}
}

// RunTerminal opens the reference interactive client over this Agent. It
// blocks until the user exits, the Agent closes, or an error occurs. Exiting
// the terminal does not close the Agent, so callers can inspect history, start
// more turns, or run the HTTP server afterward.
func (a *Agent) RunTerminal(options ...TerminalOption) error {
	if a == nil || a.runtime == nil {
		return errors.New("agent is nil")
	}
	config := terminalConfig{input: os.Stdin, output: os.Stdout, sessionID: newSessionID()}
	for index, option := range options {
		if option == nil {
			return fmt.Errorf("terminal option %d is nil", index)
		}
		if err := option(&config); err != nil {
			return fmt.Errorf("terminal option %d: %w", index, err)
		}
	}
	if a.isClosing() {
		return ErrClosed
	}
	if err := a.context.Err(); err != nil {
		return err
	}

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	modelName := "agent"
	var skills []Skill
	if a.project != nil {
		if configured := strings.TrimSpace(a.project.ModelName()); configured != "" {
			modelName = configured
		}
		skills = a.project.Skills()
	}
	client := &terminalClient{
		agent:                a,
		terminal:             newTerminal(config.output),
		modelName:            modelName,
		skills:               skills,
		sessionID:            config.sessionID,
		interrupts:           interrupts,
		pendingPermissions:   make(map[permission.ID]permission.Request),
		pendingConfirmations: make(map[confirmation.ID]confirmation.Request),
		permissionOrder:      make([]permission.ID, 0),
		permissionSubagent:   make(map[permission.ID]string),
		confirmationSubagent: make(map[confirmation.ID]string),
	}
	if config.initialPrompt != "" {
		return client.runTurn(a.context, config.initialPrompt, nil, nil)
	}
	return client.runInteractive(a.context, config.input)
}

type terminalAgent interface {
	StartSubscribed(context.Context, agentruntime.Request) (*agentruntime.Run, agentruntime.EventSubscription, error)
	SubscribeSubagentCallbacks(context.Context) <-chan SubagentCallback
	ContinueSubagentCallbackSubscribed(context.Context, SubagentCallback) (*agentruntime.Run, agentruntime.EventSubscription, error)
	ResolvePermission(context.Context, permission.Decision) error
	ResolveConfirmation(context.Context, confirmation.Decision) error
	ResolveSubagentPermission(context.Context, string, string, permission.Decision) error
	ResolveSubagentConfirmation(context.Context, string, string, confirmation.Decision) error
	SetPermissionMode(context.Context, permission.Mode) error
	PermissionMode() permission.Mode
	SubagentDefinitions() []SubagentDefinition
	ListSubagents(context.Context, string, bool) ([]storage.Subagent, error)
	ListMessages(context.Context, string) ([]agentruntime.Message, error)
	SendSubagentMessage(context.Context, string, string, string) (storage.Subagent, error)
	CloseSubagent(context.Context, string, string) (storage.Subagent, error)
	SubagentRun(context.Context, string, string, string) (*agentruntime.Run, error)
}

type terminalClient struct {
	stateMu              sync.Mutex
	renderMu             sync.Mutex
	agent                terminalAgent
	terminal             terminal
	modelName            string
	skills               []Skill
	sessionID            string
	subagentID           string
	interrupts           <-chan os.Signal
	pendingPermissions   map[permission.ID]permission.Request
	permissionOrder      []permission.ID
	permissionSubagent   map[permission.ID]string
	pendingConfirmations map[confirmation.ID]confirmation.Request
	confirmationOrder    []confirmation.ID
	confirmationSubagent map[confirmation.ID]string
	runActive            bool
	viewContext          context.Context
	viewCancel           context.CancelFunc
	rootRun              *agentruntime.Run
	rootLoading          *terminalLoadingController
	rootReplayThrough    uint64
	rootPromptQueue      []string
	rootCallbackQueue    []SubagentCallback
	rootNotices          []string
}

func (c *terminalClient) runInteractive(ctx context.Context, input io.Reader) error {
	lines, readErrors, promptManaged, closeInput, err := terminalInput(input, &c.terminal)
	if err != nil {
		return fmt.Errorf("initialize terminal input: %w", err)
	}
	defer closeInput()
	c.switchView("")
	c.terminal.banner(c.modelName, c.sessionID)
	callbacks := c.agent.SubscribeSubagentCallbacks(ctx)

	for {
		if callback, ok := c.dequeueRootCallback(); ok {
			c.rootNotice("Subagent callback", callbackDisplayReference(callback)+" · "+string(callback.Status))
			if err := c.runCallbackTurn(ctx, callback, lines, callbacks); err != nil && !errors.Is(err, agentruntime.ErrRunInterrupted) {
				if c.activeView() == "" {
					c.terminal.error(err)
				} else {
					c.rootNotice("Error", err.Error())
				}
			}
			continue
		}
		if c.activeView() == "" {
			if queuedPrompt, ok := c.dequeueRootPrompt(); ok {
				c.terminal.status("Queued message", "starting")
				if err := c.runTurn(ctx, queuedPrompt, lines, callbacks); err != nil && !errors.Is(err, agentruntime.ErrRunInterrupted) {
					if c.activeView() == "" {
						c.terminal.error(err)
					} else {
						c.rootNotice("Error", err.Error())
					}
				}
				continue
			}
		}
		if !promptManaged {
			c.terminal.prompt()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.interrupts:
			c.terminal.println("\nGoodbye.")
			return nil
		case err := <-readErrors:
			if err != nil {
				return fmt.Errorf("read prompt: %w", err)
			}
			c.terminal.println("")
			return nil
		case callback, open := <-callbacks:
			if !open {
				callbacks = nil
				continue
			}
			c.rootNotice("Subagent callback", callbackDisplayReference(callback)+" · "+string(callback.Status))
			if err := c.runCallbackTurn(ctx, callback, lines, callbacks); err != nil && !errors.Is(err, agentruntime.ErrRunInterrupted) {
				if c.activeView() == "" {
					c.terminal.error(err)
				} else {
					c.rootNotice("Error", err.Error())
				}
			}
			continue
		case line, open := <-lines:
			if !open {
				return nil
			}
			if line == terminalInterruptInput {
				if c.interruptActiveView() {
					continue
				}
				c.terminal.println("\nGoodbye.")
				return nil
			}
			prompt := strings.TrimSpace(line)
			if prompt == "" {
				continue
			}
			if handled, exit := c.command(prompt); handled {
				if exit {
					return nil
				}
				continue
			}
			if c.activeView() != "" {
				if err := c.runSubagentTurn(ctx, prompt, lines); err != nil && !errors.Is(err, agentruntime.ErrRunInterrupted) {
					c.terminal.error(err)
				}
				continue
			}
			if err := c.runTurn(ctx, prompt, lines, callbacks); err != nil && !errors.Is(err, agentruntime.ErrRunInterrupted) {
				if c.activeView() == "" {
					c.terminal.error(err)
				} else {
					c.rootNotice("Error", err.Error())
				}
			}
		}
	}
}

func (c *terminalClient) command(input string) (handled, exit bool) {
	if answer, ok := confirmationChoice(input); ok {
		if id, request, found := c.nextPendingConfirmation(); found {
			c.resolveConfirmation(id, request, answer)
			return true, false
		}
	}
	if kind, ok := permissionChoice(input); ok {
		id, request, found := c.nextPendingPermission()
		if !found {
			c.terminal.error(errors.New("no pending permission"))
			return true, false
		}
		c.resolvePermission(id, request, kind)
		return true, false
	}
	fields := strings.Fields(input)
	if len(fields) > 0 {
		switch fields[0] {
		case "/agents":
			if len(fields) != 1 {
				c.terminal.error(errors.New("usage: /agents"))
			} else {
				c.listSubagents()
			}
			return true, false
		case "/agent":
			if len(fields) != 2 {
				c.terminal.error(errors.New("usage: /agent <id-or-display-name>"))
			} else if err := c.openSubagent(fields[1]); err != nil {
				c.terminal.error(err)
			}
			return true, false
		case "/agent-status":
			if len(fields) != 2 {
				c.terminal.error(errors.New("usage: /agent-status <id-or-display-name>"))
			} else if err := c.showSubagentStatus(fields[1]); err != nil {
				c.terminal.error(err)
			}
			return true, false
		case "/back":
			if len(fields) != 1 {
				c.terminal.error(errors.New("usage: /back"))
			} else if c.activeView() == "" {
				c.terminal.status("Session", c.sessionID)
			} else {
				c.switchView("")
				c.showRootView()
			}
			return true, false
		case "/close":
			if len(fields) != 2 {
				c.terminal.error(errors.New("usage: /close <id-or-display-name>"))
			} else if err := c.closeSubagent(fields[1]); err != nil {
				c.terminal.error(err)
			}
			return true, false
		}
	}
	if len(fields) > 0 && fields[0] == "/mode" {
		if len(fields) == 1 {
			c.terminal.status("Permission mode", string(c.agent.PermissionMode()))
			return true, false
		}
		if len(fields) != 2 {
			c.terminal.error(errors.New("usage: /mode <default|acceptEdits|criticalOnly|dontAsk|plan|unrestricted>"))
			return true, false
		}
		mode, ok := parsePermissionMode(fields[1])
		if !ok {
			c.terminal.error(fmt.Errorf("unknown permission mode %q", fields[1]))
			return true, false
		}
		previous := c.agent.PermissionMode()
		if err := c.agent.SetPermissionMode(context.Background(), mode); err != nil {
			c.terminal.error(err)
			return true, false
		}
		if previous == mode {
			c.terminal.status("Permission mode", string(mode))
		} else if !c.isRunActive() {
			c.terminal.permissionMode(previous, mode)
		}
		return true, false
	}
	if input == "/permissions" {
		c.terminal.permissions(c.pendingPermissionSnapshot())
		return true, false
	}
	if input == "/confirmations" {
		c.terminal.confirmations(c.pendingConfirmationSnapshot())
		return true, false
	}
	if len(fields) == 2 && (fields[0] == "/confirm" || fields[0] == "/decline") {
		id := confirmation.ID(fields[1])
		request, ok := c.pendingConfirmation(id)
		if !ok {
			c.terminal.error(fmt.Errorf("no pending confirmation %s", id))
			return true, false
		}
		answer := confirmation.Yes
		if fields[0] == "/decline" {
			answer = confirmation.No
		}
		c.resolveConfirmation(id, request, answer)
		return true, false
	}
	if len(fields) == 2 && (fields[0] == "/allow" || fields[0] == "/allow-session" || fields[0] == "/allow-project" || fields[0] == "/deny") {
		id := permission.ID(fields[1])
		request, ok := c.pendingPermission(id)
		if !ok {
			c.terminal.error(fmt.Errorf("no pending permission %s", id))
			return true, false
		}
		kind := permission.AllowOnce
		switch fields[0] {
		case "/allow-session":
			kind = permission.AllowSession
		case "/allow-project":
			kind = permission.AllowProject
		case "/deny":
			kind = permission.Deny
		}
		c.resolvePermission(id, request, kind)
		return true, false
	}
	switch strings.ToLower(input) {
	case "/exit", "/quit":
		c.terminal.println("Goodbye.")
		return true, true
	case "/new":
		c.sessionID = newSessionID()
		c.switchView("")
		c.setRootRun(nil)
		c.clearRootPrompts()
		c.terminal.clear()
		c.terminal.banner(c.modelName, c.sessionID)
		return true, false
	case "/clear":
		c.terminal.clear()
		c.terminal.banner(c.modelName, c.sessionID)
		return true, false
	case "/session":
		if c.activeView() == "" {
			c.terminal.status("Session", c.sessionID)
		} else {
			c.terminal.status("Root session", c.sessionID)
			c.terminal.status("Subagent", c.activeView())
		}
		if c.currentViewStreaming() {
			c.terminal.status("Streaming", "active")
		} else {
			c.terminal.status("Streaming", "idle")
		}
		return true, false
	case "/skills":
		c.terminal.skills(c.skills)
		return true, false
	case "/help":
		c.terminal.help()
		return true, false
	default:
		return false, false
	}
}

// listSubagents deliberately uses ListSubagents rather than ReadSubagent:
// terminal navigation must not advance the parent model's observation cursor.
func (c *terminalClient) listSubagents() {
	instances, err := c.agent.ListSubagents(context.Background(), c.sessionID, true)
	if err != nil {
		c.terminal.error(err)
		return
	}
	c.terminal.subagents(c.agent.SubagentDefinitions(), instances)
}

func (c *terminalClient) openSubagent(id string) error {
	record, err := c.findSubagent(id)
	if err != nil {
		return err
	}
	if record.Status == storage.SubagentStatusClosed {
		return fmt.Errorf("subagent %s is closed", id)
	}
	messages, err := c.agent.ListMessages(context.Background(), record.SessionID)
	if err != nil {
		return fmt.Errorf("load subagent %s messages: %w", id, err)
	}
	viewContext := c.switchView(record.ID)
	c.terminal.clear()
	c.terminal.subagent(record)
	c.terminal.messages(messages)
	if record.Status != storage.SubagentStatusRunning && record.LastTurnError != "" {
		c.terminal.error(fmt.Errorf("subagent turn %s failed: %s", record.LastTurnID, record.LastTurnError))
	}
	if record.Status == storage.SubagentStatusRunning && record.CurrentTurnID != "" {
		c.streamSubagentRun(viewContext, c.sessionID, record.ID, record.CurrentTurnID)
	}
	return nil
}

func (c *terminalClient) closeSubagent(id string) error {
	record, err := c.findSubagent(id)
	if err != nil {
		return err
	}
	if record.Status == storage.SubagentStatusClosed {
		return fmt.Errorf("subagent %s is already closed", id)
	}
	if _, err := c.agent.CloseSubagent(context.Background(), c.sessionID, record.ID); err != nil {
		return fmt.Errorf("close subagent %s: %w", id, err)
	}
	if c.activeView() == record.ID {
		c.switchView("")
		c.showRootView()
	}
	c.terminal.status("Closed subagent", record.ID)
	return nil
}

func (c *terminalClient) findSubagent(id string) (storage.Subagent, error) {
	instances, err := c.agent.ListSubagents(context.Background(), c.sessionID, true)
	if err != nil {
		return storage.Subagent{}, err
	}
	for _, instance := range instances {
		if instance.ID == id || strings.EqualFold(instance.DisplayName, id) {
			return instance, nil
		}
	}
	return storage.Subagent{}, fmt.Errorf("subagent %s was not found in this session", id)
}

func callbackDisplayReference(callback SubagentCallback) string {
	if callback.DisplayName == "" {
		return callback.SubagentID
	}
	return callback.DisplayName + " · " + callback.SubagentID
}

func (c *terminalClient) showSubagentStatus(id string) error {
	record, err := c.findSubagent(id)
	if err != nil {
		return err
	}
	task := strings.TrimSpace(record.Label)
	if task == "" {
		task = record.DefinitionName
	}
	var activity string
	switch {
	case record.Status == storage.SubagentStatusRunning:
		activity = "Working on: " + task
	case record.LastTurnError != "":
		activity = "Last turn failed: " + record.LastTurnError
	case record.Status == storage.SubagentStatusIdle && record.LastTurnID != "":
		activity = "Completed: " + task + " · result ready"
	case record.Status == storage.SubagentStatusClosed:
		activity = "Closed: " + task
	default:
		activity = "Idle: " + task
	}
	if queued := len(record.Pending); queued != 0 {
		activity += fmt.Sprintf(" · %d follow-up message(s) queued", queued)
	}
	c.terminal.status("Subagent status", record.ID+" · "+string(record.Status)+" · "+activity)
	return nil
}

func parsePermissionMode(input string) (permission.Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "default":
		return permission.Default, true
	case "acceptedits":
		return permission.AcceptEdits, true
	case "criticalonly":
		return permission.CriticalOnly, true
	case "dontask":
		return permission.DontAsk, true
	case "plan":
		return permission.Plan, true
	case "unrestricted":
		return permission.Unrestricted, true
	default:
		return "", false
	}
}

func permissionChoice(input string) (permission.DecisionType, bool) {
	switch strings.TrimSpace(input) {
	case "1":
		return permission.AllowOnce, true
	case "2":
		return permission.AllowSession, true
	case "3":
		return permission.AllowProject, true
	case "4":
		return permission.Deny, true
	default:
		return "", false
	}
}

func confirmationChoice(input string) (confirmation.Answer, bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes":
		return confirmation.Yes, true
	case "n", "no":
		return confirmation.No, true
	default:
		return "", false
	}
}

func (c *terminalClient) nextPendingConfirmation() (confirmation.ID, confirmation.Request, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	for len(c.confirmationOrder) > 0 {
		id := c.confirmationOrder[0]
		if request, ok := c.pendingConfirmations[id]; ok {
			return id, request, true
		}
		c.confirmationOrder = c.confirmationOrder[1:]
	}
	return "", confirmation.Request{}, false
}

func (c *terminalClient) resolveConfirmation(id confirmation.ID, request confirmation.Request, answer confirmation.Answer) {
	decision := confirmation.Decision{ConfirmationID: id, SessionID: request.SessionID, TurnID: request.TurnID, CallID: request.CallID, Answer: answer}
	c.stateMu.Lock()
	subagentID := c.confirmationSubagent[id]
	c.stateMu.Unlock()
	var err error
	if subagentID != "" {
		err = c.agent.ResolveSubagentConfirmation(context.Background(), c.sessionID, subagentID, decision)
	} else {
		err = c.agent.ResolveConfirmation(context.Background(), decision)
	}
	if err != nil {
		c.terminal.error(err)
		return
	}
	c.stateMu.Lock()
	delete(c.pendingConfirmations, id)
	delete(c.confirmationSubagent, id)
	c.stateMu.Unlock()
}

func (c *terminalClient) pendingConfirmation(id confirmation.ID) (confirmation.Request, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	request, ok := c.pendingConfirmations[id]
	return request, ok
}

func (c *terminalClient) pendingConfirmationSnapshot() map[confirmation.ID]confirmation.Request {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	requests := make(map[confirmation.ID]confirmation.Request, len(c.pendingConfirmations))
	for id, request := range c.pendingConfirmations {
		requests[id] = request
	}
	return requests
}

func (c *terminalClient) nextPendingPermission() (permission.ID, permission.Request, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	for len(c.permissionOrder) > 0 {
		id := c.permissionOrder[0]
		if request, ok := c.pendingPermissions[id]; ok {
			return id, request, true
		}
		c.permissionOrder = c.permissionOrder[1:]
	}
	return "", permission.Request{}, false
}

func (c *terminalClient) resolvePermission(id permission.ID, request permission.Request, kind permission.DecisionType) {
	decision := permission.Decision{
		PermissionID: id,
		SessionID:    request.SessionID,
		TurnID:       request.TurnID,
		CallID:       request.CallID,
		Type:         kind,
	}
	c.stateMu.Lock()
	subagentID := c.permissionSubagent[id]
	c.stateMu.Unlock()
	var err error
	if subagentID != "" {
		err = c.agent.ResolveSubagentPermission(context.Background(), c.sessionID, subagentID, decision)
	} else {
		err = c.agent.ResolvePermission(context.Background(), decision)
	}
	if err != nil {
		c.terminal.error(err)
		return
	}
	c.stateMu.Lock()
	delete(c.pendingPermissions, id)
	delete(c.permissionSubagent, id)
	c.stateMu.Unlock()
}

func (c *terminalClient) pendingPermission(id permission.ID) (permission.Request, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	request, ok := c.pendingPermissions[id]
	return request, ok
}

func (c *terminalClient) pendingPermissionSnapshot() map[permission.ID]permission.Request {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	requests := make(map[permission.ID]permission.Request, len(c.pendingPermissions))
	for id, request := range c.pendingPermissions {
		requests[id] = request
	}
	return requests
}

func (c *terminalClient) setRunActive(active bool) {
	c.stateMu.Lock()
	c.runActive = active
	c.stateMu.Unlock()
}

func (c *terminalClient) isRunActive() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.runActive
}

func (c *terminalClient) switchView(subagentID string) context.Context {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	c.terminal.stopLoading()
	c.terminal.resetStream()
	c.stateMu.Lock()
	if c.viewCancel != nil {
		c.viewCancel()
	}
	viewContext, cancel := context.WithCancel(context.Background())
	c.viewContext = viewContext
	c.viewCancel = cancel
	c.subagentID = subagentID
	c.stateMu.Unlock()
	return viewContext
}

// renderInView orders view changes and stream output. Once switchView returns,
// a renderer belonging to the previous view can no longer write to the
// terminal, even if it had already received its next event.
func (c *terminalClient) renderInView(subagentID string, render func()) bool {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	if !c.isActiveView(subagentID) {
		return false
	}
	render()
	return true
}

func (c *terminalClient) activeView() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.subagentID
}

func (c *terminalClient) isActiveView(subagentID string) bool {
	return c.activeView() == subagentID
}

func (c *terminalClient) activeViewContext(subagentID string) (context.Context, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.subagentID != subagentID || c.viewContext == nil {
		return nil, false
	}
	return c.viewContext, true
}

func (c *terminalClient) interruptActiveView() bool {
	subagentID := c.activeView()
	if subagentID == "" {
		run := c.currentRootRun()
		if run == nil || run.Done() {
			return false
		}
		_ = run.Interrupt(context.Background(), "interrupted by user")
		return true
	}
	record, err := c.findSubagent(subagentID)
	if err != nil || record.Status != storage.SubagentStatusRunning || record.CurrentTurnID == "" {
		return false
	}
	run, err := c.agent.SubagentRun(context.Background(), c.sessionID, subagentID, record.CurrentTurnID)
	if err != nil || run.Done() {
		return false
	}
	_ = run.Interrupt(context.Background(), "interrupted by user")
	return true
}

func (c *terminalClient) currentViewStreaming() bool {
	subagentID := c.activeView()
	if subagentID == "" {
		run := c.currentRootRun()
		return run != nil && !run.Done()
	}
	record, err := c.findSubagent(subagentID)
	return err == nil && record.Status == storage.SubagentStatusRunning && record.CurrentTurnID != ""
}

func (c *terminalClient) setRootRun(run *agentruntime.Run) {
	c.stateMu.Lock()
	c.rootRun = run
	c.rootReplayThrough = 0
	c.stateMu.Unlock()
}

func (c *terminalClient) setRootLoading(loading *terminalLoadingController) {
	c.stateMu.Lock()
	c.rootLoading = loading
	c.stateMu.Unlock()
}

func (c *terminalClient) currentRootLoading() *terminalLoadingController {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.rootLoading
}

func (c *terminalClient) clearRootLoading(loading *terminalLoadingController) {
	c.stateMu.Lock()
	if c.rootLoading == loading {
		c.rootLoading = nil
	}
	c.stateMu.Unlock()
}

func (c *terminalClient) currentRootRun() *agentruntime.Run {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.rootRun
}

func (c *terminalClient) clearRootRun(run *agentruntime.Run) {
	c.stateMu.Lock()
	if c.rootRun == run {
		c.rootRun = nil
		c.rootReplayThrough = 0
	}
	c.stateMu.Unlock()
}

func (c *terminalClient) rootEventAlreadyReplayed(sequence uint64) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return sequence != 0 && sequence <= c.rootReplayThrough
}

func (c *terminalClient) enqueueRootPrompt(prompt string) int {
	prompt = strings.TrimSpace(prompt)
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if prompt == "" {
		return len(c.rootPromptQueue)
	}
	c.rootPromptQueue = append(c.rootPromptQueue, prompt)
	return len(c.rootPromptQueue)
}

func (c *terminalClient) dequeueRootPrompt() (string, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if len(c.rootPromptQueue) == 0 {
		return "", false
	}
	prompt := c.rootPromptQueue[0]
	c.rootPromptQueue = c.rootPromptQueue[1:]
	return prompt, true
}

func (c *terminalClient) clearRootPrompts() {
	c.stateMu.Lock()
	c.rootPromptQueue = nil
	c.stateMu.Unlock()
}

func (c *terminalClient) enqueueRootCallback(callback SubagentCallback) int {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.rootCallbackQueue = append(c.rootCallbackQueue, callback)
	return len(c.rootCallbackQueue)
}

func (c *terminalClient) dequeueRootCallback() (SubagentCallback, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if len(c.rootCallbackQueue) == 0 {
		return SubagentCallback{}, false
	}
	callback := c.rootCallbackQueue[0]
	c.rootCallbackQueue = c.rootCallbackQueue[1:]
	return callback, true
}

func (c *terminalClient) showRootView() {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	if !c.isActiveView("") {
		return
	}
	c.terminal.clear()
	c.terminal.banner(c.modelName, c.sessionID)
	messages, err := c.agent.ListMessages(context.Background(), c.sessionID)
	if err != nil {
		c.terminal.error(err)
		return
	}
	c.terminal.messages(messages)
	for _, notice := range c.takeRootNotices() {
		c.terminal.status("Notification", notice)
	}
	run := c.currentRootRun()
	if run == nil || run.Done() {
		return
	}
	wroteContent := false
	events := run.Events()
	for _, event := range events {
		c.renderBackfillEvent(event, &wroteContent)
	}
	if len(events) != 0 {
		c.stateMu.Lock()
		c.rootReplayThrough = events[len(events)-1].Sequence
		c.stateMu.Unlock()
	}
	if loading := c.currentRootLoading(); loading != nil && !wroteContent {
		loading.Start("Thinking")
	}
}

func (c *terminalClient) rootNotice(label, value string) {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	c.stateMu.Lock()
	visible := c.subagentID == ""
	if !visible {
		c.rootNotices = append(c.rootNotices, label+" · "+value)
	}
	c.stateMu.Unlock()
	if visible {
		c.terminal.status(label, value)
	}
}

func (c *terminalClient) takeRootNotices() []string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	notices := append([]string(nil), c.rootNotices...)
	c.rootNotices = nil
	return notices
}

func (c *terminalClient) renderBackfillEvent(event agentruntime.AgentEvent, wroteContent *bool) {
	if event.Type != agentruntime.ProviderEventReceived {
		return
	}
	switch event.ProviderEvent.Type {
	case provider.ContentReceived:
		c.terminal.write(event.ProviderEvent.Content)
		*wroteContent = true
	case provider.ReasoningReceived:
		c.terminal.reasoning(event.ProviderEvent.Reasoning)
	}
}

func (c *terminalClient) runTurn(ctx context.Context, prompt string, input <-chan string, callbacks <-chan SubagentCallback) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	return c.runRootTurn(ctx, input, callbacks, func() (*agentruntime.Run, agentruntime.EventSubscription, error) {
		return c.agent.StartSubscribed(ctx, agentruntime.Request{
			SessionID: c.sessionID,
			Message: agentruntime.Message{
				Type:    agentruntime.MessageTypeUser,
				Content: prompt,
			},
		})
	})
}

func (c *terminalClient) runCallbackTurn(ctx context.Context, callback SubagentCallback, input <-chan string, callbacks <-chan SubagentCallback) error {
	return c.runRootTurn(ctx, input, callbacks, func() (*agentruntime.Run, agentruntime.EventSubscription, error) {
		return c.agent.ContinueSubagentCallbackSubscribed(ctx, callback)
	})
}

func (c *terminalClient) runRootTurn(ctx context.Context, input <-chan string, callbacks <-chan SubagentCallback, start func() (*agentruntime.Run, agentruntime.EventSubscription, error)) error {
	run, subscription, err := start()
	if err != nil {
		return err
	}
	c.setRootRun(run)
	defer c.clearRootRun(run)
	c.setRunActive(true)
	defer c.setRunActive(false)
	loading := c.terminal.loadingController()
	c.setRootLoading(loading)
	defer c.clearRootLoading(loading)
	if c.activeView() == "" {
		loading.Start("Thinking")
	}
	defer loading.Stop()

	wroteContent := false
	for {
		select {
		case <-c.interrupts:
			if !c.interruptActiveView() {
				if err := run.Interrupt(context.Background(), "interrupted by user"); err != nil && !errors.Is(err, agentruntime.ErrRunNotFound) {
					return err
				}
			}
		case line, open := <-input:
			if !open {
				input = nil
				continue
			}
			if line == terminalInterruptInput {
				if !c.interruptActiveView() {
					if err := run.Interrupt(context.Background(), "interrupted by user"); err != nil && !errors.Is(err, agentruntime.ErrRunNotFound) {
						return err
					}
				}
				continue
			}
			value := strings.TrimSpace(line)
			if value == "" {
				continue
			}
			if handled, _ := c.command(value); handled {
				continue
			}
			if c.activeView() != "" {
				if err := c.runSubagentTurn(ctx, value, input); err != nil {
					c.terminal.error(err)
				}
				continue
			}
			position := c.enqueueRootPrompt(value)
			c.terminal.status("Queued message", fmt.Sprintf("%d waiting for main agent", position))
		case callback, open := <-callbacks:
			if !open {
				callbacks = nil
				continue
			}
			position := c.enqueueRootCallback(callback)
			c.rootNotice("Subagent callback", fmt.Sprintf("%s · %s · %d waiting", callback.SubagentID, callback.Status, position))
		case event, open := <-subscription.Events:
			if !open {
				// Always flush: this view may have reconstructed content from
				// retained events while its live subscription was detached.
				c.renderInView("", func() { c.terminal.println("") })
				_, err := run.Result()
				return err
			}
			if c.rootEventAlreadyReplayed(event.Sequence) {
				c.observeEvent(event)
				continue
			}
			if !c.renderInView("", func() { c.renderEventWithLoading(event, &wroteContent, loading) }) {
				c.observeEvent(event)
			}
		}
	}
}

func (c *terminalClient) runSubagentTurn(ctx context.Context, prompt string, input <-chan string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	subagentID := c.activeView()
	if subagentID == "" {
		return errors.New("no active subagent view")
	}
	record, err := c.findSubagent(subagentID)
	if err != nil {
		return err
	}
	if record.Status == storage.SubagentStatusClosed {
		return fmt.Errorf("subagent %s is closed", record.ID)
	}
	sent, err := c.agent.SendSubagentMessage(ctx, c.sessionID, record.ID, prompt)
	if err != nil {
		return err
	}
	if record.Status == storage.SubagentStatusRunning {
		c.terminal.status("Queued message", sent.ID)
		return nil
	}
	c.terminal.status("Subagent", string(sent.Status))
	viewContext, active := c.activeViewContext(sent.ID)
	if active {
		c.streamSubagentRun(viewContext, c.sessionID, sent.ID, sent.CurrentTurnID)
	}
	return nil
}

func (c *terminalClient) streamSubagentRun(ctx context.Context, parentSessionID, id, turnID string) {
	go func() {
		if err := c.renderSubagentRun(ctx, parentSessionID, id, turnID, nil); err != nil && !errors.Is(err, agentruntime.ErrRunNotFound) && !errors.Is(err, agentruntime.ErrRunInterrupted) && !errors.Is(err, context.Canceled) {
			c.renderInView(id, func() { c.terminal.error(err) })
		}
	}()
}

// renderSubagentRun uses the retained run only for event display. Transcript
// reads above use ListMessages, so terminal activity never marks a child as
// observed by the parent LLM.
func (c *terminalClient) renderSubagentRun(ctx context.Context, parentSessionID, id, turnID string, input <-chan string) error {
	if turnID == "" {
		return nil
	}
	run, err := c.agent.SubagentRun(ctx, parentSessionID, id, turnID)
	if err != nil {
		return err
	}
	subscription := run.Subscribe(ctx)
	events, err := run.EventsBetween(agentruntime.EventCursor{}, subscription.Cursor)
	if err != nil {
		return err
	}
	c.setRunActive(true)
	defer c.setRunActive(false)
	wroteContent := false
	for _, event := range events {
		if !c.renderInView(id, func() { c.renderBackfillEvent(event, &wroteContent) }) {
			return nil
		}
	}
	loading := c.terminal.loadingController()
	if !wroteContent {
		loading.Start("Thinking")
	}
	defer loading.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.interrupts:
			if err := run.Interrupt(context.Background(), "interrupted by user"); err != nil && !errors.Is(err, agentruntime.ErrRunNotFound) {
				return err
			}
		case line, open := <-input:
			if !open {
				input = nil
				continue
			}
			value := strings.TrimSpace(line)
			if line == terminalInterruptInput {
				if err := run.Interrupt(context.Background(), "interrupted by user"); err != nil && !errors.Is(err, agentruntime.ErrRunNotFound) {
					return err
				}
				continue
			}
			if value == "" {
				continue
			}
			if handled, _ := c.command(value); handled {
				continue
			}
			queued, sendErr := c.agent.SendSubagentMessage(ctx, parentSessionID, id, value)
			if sendErr != nil {
				c.terminal.error(sendErr)
			} else {
				c.terminal.status("Queued message", queued.ID)
			}
		case event, open := <-subscription.Events:
			if !open {
				if ctx.Err() != nil {
					return nil
				}
				c.renderInView(id, func() { c.terminal.println("") })
				_, err := run.Result()
				return err
			}
			c.renderInView(id, func() { c.renderSubagentEventWithLoading(id, event, &wroteContent, loading) })
		}
	}
}

func (c *terminalClient) renderEvent(event agentruntime.AgentEvent, wroteContent *bool) bool {
	return c.renderEventForSubagent("", event, wroteContent, true, nil)
}

func (c *terminalClient) renderSubagentEvent(subagentID string, event agentruntime.AgentEvent, wroteContent *bool) bool {
	return c.renderEventForSubagent(subagentID, event, wroteContent, true, nil)
}

func (c *terminalClient) renderEventWithLoading(event agentruntime.AgentEvent, wroteContent *bool, loading *terminalLoadingController) bool {
	return c.renderEventForSubagent("", event, wroteContent, true, loading)
}

func (c *terminalClient) renderSubagentEventWithLoading(subagentID string, event agentruntime.AgentEvent, wroteContent *bool, loading *terminalLoadingController) bool {
	return c.renderEventForSubagent(subagentID, event, wroteContent, true, loading)
}

func (c *terminalClient) observeEvent(event agentruntime.AgentEvent) {
	wroteContent := false
	c.renderEventForSubagent("", event, &wroteContent, false, nil)
}

func (c *terminalClient) renderEventForSubagent(subagentID string, event agentruntime.AgentEvent, wroteContent *bool, visible bool, loading *terminalLoadingController) bool {
	switch event.Type {
	case agentruntime.RunStarted:
		if visible && event.PermissionMode != nil && event.PermissionMode.Current == permission.Unrestricted {
			c.terminal.unrestricted()
		}
	case agentruntime.ProviderEventReceived:
		if !visible {
			break
		}
		switch event.ProviderEvent.Type {
		case provider.ContentReceived:
			loading.Stop()
			c.terminal.write(event.ProviderEvent.Content)
			*wroteContent = true
		case provider.ReasoningReceived:
			loading.Stop()
			c.terminal.reasoning(event.ProviderEvent.Reasoning)
		}
	case agentruntime.ToolCallRequested:
		if visible && event.ToolRequest != nil {
			loading.Stop()
			c.terminal.ensureNewline(*wroteContent)
			*wroteContent = false
			c.terminal.toolCall(event.ToolRequest.Call.Name, "")
			loading.Start("Running " + event.ToolRequest.Call.Name)
		}
	case agentruntime.ToolResultReceived:
		if event.ToolResult != nil {
			if visible {
				loading.Stop()
				c.terminal.toolResult(event.ToolResult.Result)
				if event.ToolResult.Result.Name == SubagentStatusToolName {
					c.terminal.subagentStatus(event.ToolResult.Result.Output)
				}
			}
			if visible {
				loading.Start("Thinking")
			}
		}
	case agentruntime.AgentInterrupted:
		if visible {
			loading.Stop()
			c.terminal.ensureNewline(*wroteContent)
			*wroteContent = false
			c.terminal.interrupted()
		}
	case agentruntime.AgentPermissionRequested:
		if event.Permission != nil {
			if visible {
				loading.Stop()
			}
			c.stateMu.Lock()
			if _, exists := c.pendingPermissions[event.Permission.ID]; !exists {
				c.permissionOrder = append(c.permissionOrder, event.Permission.ID)
			}
			c.pendingPermissions[event.Permission.ID] = *event.Permission
			if subagentID != "" {
				if c.permissionSubagent == nil {
					c.permissionSubagent = make(map[permission.ID]string)
				}
				c.permissionSubagent[event.Permission.ID] = subagentID
			}
			c.stateMu.Unlock()
			if visible {
				c.terminal.permission(*event.Permission)
			}
		}
	case agentruntime.AgentPermissionResolved, agentruntime.AgentPermissionCancelled, agentruntime.AgentPermissionExpired:
		c.stateMu.Lock()
		if event.Permission != nil {
			delete(c.pendingPermissions, event.Permission.ID)
			delete(c.permissionSubagent, event.Permission.ID)
		}
		if event.Decision != nil {
			delete(c.pendingPermissions, event.Decision.PermissionID)
			delete(c.permissionSubagent, event.Decision.PermissionID)
		}
		c.stateMu.Unlock()
		if visible {
			loading.Start("Thinking")
		}
	case agentruntime.AgentConfirmationRequested:
		if event.Confirmation != nil {
			if visible {
				loading.Stop()
			}
			c.stateMu.Lock()
			if _, exists := c.pendingConfirmations[event.Confirmation.ID]; !exists {
				c.confirmationOrder = append(c.confirmationOrder, event.Confirmation.ID)
			}
			c.pendingConfirmations[event.Confirmation.ID] = *event.Confirmation
			if subagentID != "" {
				if c.confirmationSubagent == nil {
					c.confirmationSubagent = make(map[confirmation.ID]string)
				}
				c.confirmationSubagent[event.Confirmation.ID] = subagentID
			}
			c.stateMu.Unlock()
			if visible {
				c.terminal.confirmation(*event.Confirmation)
			}
		}
	case agentruntime.AgentConfirmationResolved, agentruntime.AgentConfirmationCancelled, agentruntime.AgentConfirmationExpired:
		c.stateMu.Lock()
		if event.Confirmation != nil {
			delete(c.pendingConfirmations, event.Confirmation.ID)
			delete(c.confirmationSubagent, event.Confirmation.ID)
		}
		if event.ConfirmationDecision != nil {
			delete(c.pendingConfirmations, event.ConfirmationDecision.ConfirmationID)
			delete(c.confirmationSubagent, event.ConfirmationDecision.ConfirmationID)
		}
		c.stateMu.Unlock()
		if visible {
			loading.Start("Thinking")
		}
	case agentruntime.RunCompleted, agentruntime.RunFailed:
		if visible {
			loading.Stop()
		}
	case agentruntime.PermissionModeChanged:
		if visible && event.PermissionMode != nil {
			c.terminal.ensureNewline(*wroteContent)
			*wroteContent = false
			c.terminal.permissionMode(event.PermissionMode.Previous, event.PermissionMode.Current)
		}
	}
	return true
}

func scanLines(input io.Reader) (<-chan string, <-chan error) {
	lines := make(chan string)
	errorsChannel := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 1024), 1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		errorsChannel <- scanner.Err()
		close(errorsChannel)
	}()
	return lines, errorsChannel
}

func terminalInput(input io.Reader, output *terminal) (<-chan string, <-chan error, bool, func(), error) {
	inputFile, inputIsFile := input.(*os.File)
	if output == nil || !output.interactive || !inputIsFile {
		lines, readErrors := scanLines(input)
		return lines, readErrors, false, func() {}, nil
	}
	inputInfo, err := inputFile.Stat()
	if err != nil || inputInfo.Mode()&os.ModeCharDevice == 0 {
		lines, readErrors := scanLines(input)
		return lines, readErrors, false, func() {}, nil
	}
	instance, err := readline.NewEx(&readline.Config{
		Prompt:          output.promptValue(),
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
		Stdin:           inputFile,
		Stdout:          output.out,
		Stderr:          output.out,
	})
	if err != nil {
		return nil, nil, false, func() {}, err
	}
	output.loading.attach(instance, output.promptValue())
	output.stream.attach(readline.GetScreenWidth)
	output.out = instance.Stdout()
	lines := make(chan string)
	readErrors := make(chan error, 1)
	go func() {
		defer close(lines)
		defer close(readErrors)
		for {
			line, readErr := instance.Readline()
			switch {
			case errors.Is(readErr, readline.ErrInterrupt):
				lines <- terminalInterruptInput
				continue
			case errors.Is(readErr, io.EOF):
				readErrors <- nil
				return
			case readErr != nil:
				readErrors <- readErr
				return
			}
			lines <- line
		}
	}()
	return lines, readErrors, true, func() {
		output.stopLoading()
		output.stream.detach()
		output.loading.detach(instance)
		_ = instance.Close()
	}, nil
}

func newSessionID() string {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err == nil {
		return "session_" + hex.EncodeToString(random)
	}
	return fmt.Sprintf("session_%x", time.Now().UnixNano())
}

type terminal struct {
	out         io.Writer
	color       bool
	interactive bool
	stream      *terminalStreamRenderer
	loading     *terminalLoadingState
}

func newTerminal(output io.Writer) terminal {
	outputFile, isFile := output.(*os.File)
	interactive := false
	if isFile {
		info, err := outputFile.Stat()
		interactive = err == nil && info.Mode()&os.ModeCharDevice != 0
	}
	value := terminal{out: output, color: interactive, interactive: interactive}
	if interactive {
		value.stream = &terminalStreamRenderer{}
		value.loading = &terminalLoadingState{}
	}
	return value
}

func (t terminal) banner(model, sessionID string) {
	t.println(t.paint("1;36", "╭─ agentcli"))
	t.println(t.paint("2", "│  "+model+" · "+sessionID))
	t.println(t.paint("1;36", "╰─") + "  " + t.paint("2", "Type /help for commands"))
}

func (t terminal) prompt() {
	fmt.Fprint(t.out, "\n"+t.promptValue())
}

func (t terminal) promptValue() string { return t.paint("1;36", "❯ ") }

func (t terminal) help() {
	t.println(t.paint("1", "Commands"))
	t.println("  /agents   list available definitions and child sessions")
	t.println("  /agent REF enter an open child session by ID or display name")
	t.println("  /agent-status REF show child status without an agent turn")
	t.println("  /back     return to the root session")
	t.println("  /close REF close a child session by ID or display name")
	t.println("  /new      start a new conversation")
	t.println("  /session  show the current session ID")
	t.println("  /skills   show skills available for automatic selection")
	t.println("  /clear    clear and redraw the terminal")
	t.println("  /exit     quit")
	t.println("  /allow ID allow a pending permission once")
	t.println("  /allow-session ID allow this capability for the session")
	t.println("  /allow-project ID allow this capability for the project")
	t.println("  /deny ID  deny a pending permission")
	t.println("  /permissions list pending permissions")
	t.println("  /confirmations list pending Yes/No confirmations")
	t.println("  /confirm ID answer Yes to a confirmation")
	t.println("  /decline ID answer No to a confirmation")
	t.println("  /mode     show the current permission mode")
	t.println("  /mode MODE change the permission mode")
	t.println("  1-4       answer the oldest pending permission")
	t.println("  y/n       answer the oldest pending confirmation")
	t.println("  Ctrl+C    interrupt an active response")
}

func (t terminal) skills(skills []Skill) {
	if len(skills) == 0 {
		t.println("No skills discovered.")
		return
	}
	for _, skill := range skills {
		t.println(skill.Name + " · " + skill.Description)
	}
}

func (t terminal) subagents(definitions []SubagentDefinition, instances []storage.Subagent) {
	if len(definitions) == 0 && len(instances) == 0 {
		t.println("No subagent definitions or sessions available.")
		return
	}
	if len(definitions) != 0 {
		t.println(t.paint("1", "Available subagents"))
		for _, definition := range definitions {
			skills := "none"
			if len(definition.Skills) != 0 {
				skills = strings.Join(definition.Skills, ",")
			}
			tools := "none"
			if len(definition.Tools) != 0 {
				tools = strings.Join(definition.Tools, ",")
			}
			details := " · " + definition.Provider + "/" + definition.Model + " · skills=" + skills + " · tools=" + tools
			t.println("  " + definition.Name + " · " + definition.Description + t.paint("2", details))
		}
	}
	if len(instances) != 0 {
		t.println(t.paint("1", "Child sessions"))
		for _, instance := range instances {
			label := instance.DisplayName + " · " + instance.DefinitionName
			if instance.Label != "" {
				label += " · " + instance.Label
			}
			queued := ""
			if len(instance.Pending) != 0 {
				queued = fmt.Sprintf(" · %d queued", len(instance.Pending))
			}
			t.println("  " + instance.ID + " · " + label + " · " + string(instance.Status) + queued)
		}
	}
}

func (t terminal) subagent(instance storage.Subagent) {
	label := instance.DisplayName + " · " + instance.DefinitionName
	if instance.Label != "" {
		label += " · " + instance.Label
	}
	t.status("Subagent", label+" · "+instance.ID+" · "+string(instance.Status))
}

func (t terminal) subagentStatus(output json.RawMessage) {
	var result struct {
		Subagent struct {
			ID             string                 `json:"id"`
			Status         storage.SubagentStatus `json:"status"`
			QueuedMessages int                    `json:"queued_messages"`
		} `json:"subagent"`
		ActivitySummary string `json:"activity_summary"`
		ResultReady     bool   `json:"result_ready"`
	}
	if json.Unmarshal(output, &result) != nil || result.Subagent.ID == "" {
		return
	}
	details := string(result.Subagent.Status) + " · " + result.ActivitySummary
	if result.ResultReady {
		details += " · result ready"
	}
	t.status("Subagent status", result.Subagent.ID+" · "+details)
}

func (t terminal) messages(messages []agentruntime.Message) {
	for _, message := range messages {
		switch message.Type {
		case agentruntime.MessageTypeUser:
			t.println(t.paint("36", "You · ") + message.Content)
		case agentruntime.MessageTypeAssistant:
			t.println(t.paint("32", "Agent · ") + message.Content)
		case agentruntime.MessageTypeToolCall:
			for _, call := range message.ToolCalls {
				t.toolCall(call.Name, "")
			}
		case agentruntime.MessageTypeToolResult:
			if message.ToolResult != nil {
				t.toolResult(*message.ToolResult)
			}
		}
	}
}

func (t terminal) permission(request permission.Request) {
	t.println(t.paint("33", "⚠ permission ") + request.ToolName + " · " + request.Details)
	t.println(t.paint("2", "  "+string(request.ID)))
	t.println("  1. Allow once")
	t.println("  2. Allow for this session")
	t.println("  3. Allow for this project")
	t.println("  4. Deny")
	t.println(t.paint("2", "  Type 1-4 and press Enter; /allow ID also works."))
}

func (t terminal) confirmation(request confirmation.Request) {
	title := strings.TrimSpace(request.Title)
	if title == "" {
		title = "Confirmation required"
	}
	t.println(t.paint("36", "? "+title+" · ") + request.ToolName)
	if strings.TrimSpace(request.Details) != "" {
		t.println("  " + request.Details)
	}
	t.println("  " + request.Message)
	t.println(t.paint("2", "  "+string(request.ID)))
	t.println("  Yes")
	t.println("  No")
	t.println(t.paint("2", "  Type y/n and press Enter; /confirm ID or /decline ID also works."))
}

func (t terminal) unrestricted() {
	t.println(t.paint("31", "⚠ unrestricted · full host access"))
}

func (t terminal) permissionMode(previous, current permission.Mode) {
	color := "36"
	if current == permission.Unrestricted {
		color = "31"
	}
	t.println(t.paint(color, "Permission mode · ") + string(previous) + " → " + string(current))
}

func (t terminal) permissions(requests map[permission.ID]permission.Request) {
	if len(requests) == 0 {
		t.println("No pending permissions.")
		return
	}
	for id, request := range requests {
		t.println(string(id) + " · " + request.ToolName + " · " + request.Details)
	}
}

func (t terminal) confirmations(requests map[confirmation.ID]confirmation.Request) {
	if len(requests) == 0 {
		t.println("No pending confirmations.")
		return
	}
	for id, request := range requests {
		t.println(string(id) + " · " + request.ToolName + " · " + request.Message)
	}
}

func (t terminal) toolCall(name, details string) {
	label := "● " + name
	if details != "" {
		label += " (" + details + ")"
	}
	t.println(t.paint("33", label))
}

func (t terminal) toolResult(result agentruntime.ToolResult) {
	icon := "✓"
	color := "32"
	detail := "done"
	if result.Status != agentruntime.ToolResultSucceeded {
		icon = "✗"
		color = "31"
		detail = result.Error
	}
	t.println(t.paint(color, "  "+icon+" "+result.Name) + t.paint("2", " · "+detail))
}

func (t terminal) reasoning(reasoning string) {
	if strings.TrimSpace(reasoning) != "" {
		t.println(t.paint("2", "\nthinking · "+reasoning))
	}
}

func (t terminal) status(label, value string) {
	t.println(t.paint("36", label+" · ") + value)
}

func (t terminal) interrupted() {
	t.println(t.paint("33", "Interrupted."))
}

func (t terminal) error(err error) {
	if err != nil {
		t.println(t.paint("31", "Error · "+err.Error()))
	}
}

func (t terminal) clear() {
	t.resetStream()
	if t.color {
		fmt.Fprint(t.out, "\033[2J\033[H")
	}
}

func (t terminal) ensureNewline(condition bool) {
	if condition {
		t.println("")
	}
}

func (t terminal) write(value string) {
	t.stopLoading()
	if t.stream != nil && t.stream.write(t.out, value) {
		return
	}
	fmt.Fprint(t.out, value)
}

func (t terminal) println(value string) {
	if t.stream != nil && t.stream.commit() && value == "" {
		return
	}
	fmt.Fprintln(t.out, value)
}

func (t terminal) resetStream() {
	if t.stream != nil {
		t.stream.reset()
	}
}

func (t terminal) paint(code, value string) string {
	if !t.color {
		return value
	}
	return "\033[" + code + "m" + value + "\033[0m"
}
