package toolexecution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/mrbryside/agentcli/agentruntime"
	"github.com/mrbryside/agentcli/confirmation"
	"github.com/mrbryside/agentcli/permission"
	"github.com/mrbryside/agentcli/storage"
	"github.com/mrbryside/agentcli/storage/inmemory"
)

// Executor consumes shared tool requests with a bounded worker pool.
type Executor struct {
	registry            *Registry
	workers             int
	toolCallGuardModels map[string]agentruntime.Model

	mu     sync.Mutex
	active map[callKey]map[*activeCall]struct{}
	config Config
	policy *PermissionController
}

// Config owns permission transports and policy; nil transports preserve legacy execution.
type Config struct {
	// PermissionEnabled makes missing or unusable transports a configuration error.
	// Leaving it false is the explicit legacy/no-admission path.
	PermissionEnabled   bool
	NonInteractive      bool
	PermissionRequests  chan<- permission.Request
	PermissionDecisions <-chan permission.Decision
	Policy              permission.Policy
	// PermissionController supplies immutable policy snapshots. Each request
	// captures one at admission; when nil, NewExecutor creates one from Policy
	// for backwards compatibility.
	PermissionController  *PermissionController
	Store                 storage.PermissionStorage
	PermissionTimeout     time.Duration
	ConfirmationEnabled   bool
	ConfirmationRequests  chan<- confirmation.Request
	ConfirmationDecisions <-chan confirmation.Decision
	ConfirmationStore     storage.ConfirmationStorage
	ConfirmationTimeout   time.Duration
	Now                   func() time.Time
	After                 func(time.Duration) <-chan time.Time
	ProjectID             string
	// ToolCallGuardModel evaluates prompt-guarded tools that do not select a
	// provider/model pair.
	ToolCallGuardModel agentruntime.Model
	// ToolCallGuardModelResolver resolves an explicit provider/model pair on
	// a prompt-guarded tool. Agent construction supplies a project resolver.
	ToolCallGuardModelResolver func(providerName, modelName string) (agentruntime.Model, error)
}

type callKey struct {
	sessionID string
	turnID    string
	callID    string
}

type activeCall struct {
	cancel context.CancelCauseFunc
}

type workerJob struct {
	request   agentruntime.ToolRequest
	admission policySnapshot
	ctx       context.Context
	active    *activeCall
}

type pendingPermission struct {
	request   agentruntime.ToolRequest
	admission policySnapshot
	prompt    permission.Request
	confirm   confirmation.Description
}

type pendingConfirmation struct {
	request   agentruntime.ToolRequest
	admission policySnapshot
	prompt    confirmation.Request
}

// deferredApproval keeps approval-requiring calls in provider order without
// publishing more than one permission or confirmation prompt at a time.
// Permission descriptions are re-evaluated after earlier decisions so a new
// session/project grant can admit later calls without redundant prompts.
type deferredApproval struct {
	request    agentruntime.ToolRequest
	admission  policySnapshot
	permission *permission.Description
	confirm    confirmation.Description
}

var errConfirmationDeclined = errors.New("confirmation declined")

// NewExecutor creates a tool executor with workerCount parallel workers.
func NewExecutor(registry *Registry, workerCount int, configs ...Config) (*Executor, error) {
	if registry == nil {
		return nil, errors.New("tool registry is required")
	}
	if workerCount <= 0 {
		return nil, fmt.Errorf("worker count must be positive: %d", workerCount)
	}
	config := Config{}
	if len(configs) > 0 {
		config = configs[0]
	}
	if config.Store == nil {
		config.Store = inmemory.NewPermissionStorage()
	}
	if config.ConfirmationStore == nil {
		config.ConfirmationStore = inmemory.NewConfirmationStorage()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.After == nil {
		config.After = time.After
	}
	if config.PermissionEnabled && (config.PermissionRequests == nil || config.PermissionDecisions == nil || cap(config.PermissionRequests) == 0 || cap(config.PermissionDecisions) == 0) {
		return nil, errors.New("enabled permission admission requires buffered request and decision channels")
	}
	if config.ConfirmationEnabled && (config.ConfirmationRequests == nil || config.ConfirmationDecisions == nil || cap(config.ConfirmationRequests) == 0 || cap(config.ConfirmationDecisions) == 0) {
		return nil, errors.New("enabled confirmation admission requires buffered request and decision channels")
	}
	toolCallGuardModels := make(map[string]agentruntime.Model)
	for _, guard := range registry.promptCallGuards() {
		model := config.ToolCallGuardModel
		if guard.providerName != "" {
			if config.ToolCallGuardModelResolver == nil {
				return nil, fmt.Errorf("tool %q prompt guard selects provider %q and model %q but no model resolver is configured", guard.toolName, guard.providerName, guard.modelName)
			}
			var err error
			model, err = config.ToolCallGuardModelResolver(guard.providerName, guard.modelName)
			if err != nil {
				return nil, fmt.Errorf("resolve tool %q prompt guard model: %w", guard.toolName, err)
			}
		}
		if isNilGuardModel(model) {
			return nil, fmt.Errorf("tool %q prompt guard requires a guard model", guard.toolName)
		}
		toolCallGuardModels[guard.toolName] = model
	}
	controller := config.PermissionController
	if controller == nil {
		var err error
		controller, err = NewPermissionController(config.Policy)
		if err != nil {
			return nil, err
		}
	}
	return &Executor{
		registry:            registry,
		workers:             workerCount,
		toolCallGuardModels: toolCallGuardModels,
		active:              make(map[callKey]map[*activeCall]struct{}),
		config:              config,
		policy:              controller,
	}, nil
}

func isNilGuardModel(model agentruntime.Model) bool {
	if model == nil {
		return true
	}
	value := reflect.ValueOf(model)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// PermissionMode returns the current executor admission mode.
func (e *Executor) PermissionMode() permission.Mode {
	if e == nil {
		return ""
	}
	return e.policy.Policy().Mode
}

// SetPermissionMode updates future tool-request admission while preserving
// explicit policy rules. Existing pending prompts keep their admission
// snapshot and remain pending.
func (e *Executor) SetPermissionMode(mode permission.Mode) error {
	if e == nil {
		return errors.New("executor is nil")
	}
	return e.policy.SetMode(mode)
}

// Run dispatches requests, applies correlated interrupts, and waits for every
// worker before returning. Closing requests initiates an orderly shutdown.
func (e *Executor) Run(ctx context.Context, requests <-chan agentruntime.ToolRequest, results chan<- agentruntime.ToolResultEnvelope, interrupts <-chan agentruntime.ToolInterrupt) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if requests == nil {
		return nil
	}

	jobs := make(chan workerJob, e.workers)
	var workers sync.WaitGroup
	workers.Add(e.workers)
	for range e.workers {
		go e.worker(ctx, jobs, results, &workers)
	}

	var pending []workerJob
	pendingPermissions := map[permission.ID]pendingPermission{}
	pendingConfirmations := map[confirmation.ID]pendingConfirmation{}
	deferredApprovals := []deferredApproval{}
	expiredPermissions := make(chan permission.ID, 64)
	expiredConfirmations := make(chan confirmation.ID, 64)
	grants := []permissionGrant{}
	decisions := e.config.PermissionDecisions
	confirmationDecisions := e.config.ConfirmationDecisions
	for requests != nil || len(pending) != 0 || len(pendingPermissions) != 0 || len(pendingConfirmations) != 0 || len(deferredApprovals) != 0 {
		for {
			progressed := false
			for index := 0; index < len(deferredApprovals); {
				deferred := deferredApprovals[index]
				if approvalPendingForSession(pendingPermissions, pendingConfirmations, deferred.request.SessionID) {
					index++
					continue
				}
				deferredApprovals = append(deferredApprovals[:index], deferredApprovals[index+1:]...)
				if job, ready := e.admitDeferredApproval(
					ctx,
					deferred,
					grants,
					decisions != nil,
					confirmationDecisions != nil,
					pendingPermissions,
					pendingConfirmations,
					expiredPermissions,
					expiredConfirmations,
					results,
				); ready {
					pending = append(pending, job)
				}
				progressed = true
			}
			if !progressed {
				break
			}
		}

		var jobChannel chan<- workerJob
		var next workerJob
		if len(pending) != 0 {
			jobChannel = jobs
			next = pending[0]
		}

		select {
		case <-ctx.Done():
			e.cancelPendingPermissions(pendingPermissions, results)
			e.cancelPendingConfirmations(pendingConfirmations)
			e.shutdown(ctx, jobs, &workers)
			return nil
		case <-expiredPermissions:
			for _, record := range e.config.Store.Expire(e.config.Now().UTC()) {
				if pending, found := pendingPermissions[record.Request.ID]; found {
					delete(pendingPermissions, record.Request.ID)
					e.sendResult(ctx, results, deniedResult(pending.request))
				}
			}
		case <-expiredConfirmations:
			for _, record := range e.config.ConfirmationStore.Expire(e.config.Now().UTC()) {
				if pendingConfirmation, found := pendingConfirmations[record.Request.ID]; found {
					delete(pendingConfirmations, record.Request.ID)
					e.sendResult(ctx, results, declinedResult(pendingConfirmation.request, "confirmation expired"))
				}
			}
		case decision, ok := <-decisions:
			if !ok {
				decisions = nil
				e.denyPendingPermissions(ctx, pendingPermissions, results)
				continue
			}
			pendingPermission, found := pendingPermissions[decision.PermissionID]
			request := pendingPermission.request
			if !found || decision.SessionID != request.SessionID || decision.TurnID != request.TurnID || decision.CallID != request.Call.CallID {
				continue
			}
			if _, err := e.config.Store.Resolve(decision); err != nil {
				continue
			}
			delete(pendingPermissions, decision.PermissionID)
			if decision.Type == permission.Deny {
				e.sendResult(ctx, results, deniedResult(request))
				continue
			}
			if decision.Type == permission.AllowSession || decision.Type == permission.AllowProject {
				grants = append(grants, permissionGrant{scope: decision.Type, sessionID: request.SessionID, projectID: e.config.ProjectID, actions: append([]permission.Action(nil), pendingPermission.prompt.Actions...)})
			}
			if pendingPermission.confirm.Message != "" && confirmationDecisions == nil {
				e.sendResult(ctx, results, declinedResult(request, "confirmation channel closed"))
				continue
			}
			job, ready, confirmErr := e.admitConfirmation(ctx, request, pendingPermission.admission, pendingPermission.confirm, pendingConfirmations, expiredConfirmations)
			if confirmErr != nil {
				e.sendResult(ctx, results, confirmationAdmissionResult(request, confirmErr))
			} else if ready {
				pending = append(pending, job)
			}
		case decision, ok := <-confirmationDecisions:
			if !ok {
				confirmationDecisions = nil
				e.declinePendingConfirmations(ctx, pendingConfirmations, results, "confirmation channel closed")
				continue
			}
			pendingConfirmation, found := pendingConfirmations[decision.ConfirmationID]
			request := pendingConfirmation.request
			if !found || decision.SessionID != request.SessionID || decision.TurnID != request.TurnID || decision.CallID != request.Call.CallID {
				continue
			}
			if _, err := e.config.ConfirmationStore.Resolve(decision); err != nil {
				continue
			}
			delete(pendingConfirmations, decision.ConfirmationID)
			if decision.Answer == confirmation.No {
				e.sendResult(ctx, results, declinedResult(request, "user declined confirmation"))
				continue
			}
			pending = append(pending, e.newJob(ctx, request, pendingConfirmation.admission))
		case interrupt, ok := <-interrupts:
			if !ok {
				interrupts = nil
				continue
			}
			e.interrupt(interrupt)
			for id, pendingPermission := range pendingPermissions {
				request := pendingPermission.request
				if request.SessionID == interrupt.SessionID && request.TurnID == interrupt.TurnID {
					e.config.Store.Cancel(interrupt.SessionID, interrupt.TurnID)
					delete(pendingPermissions, id)
				}
			}
			for id, pendingConfirmation := range pendingConfirmations {
				request := pendingConfirmation.request
				if request.SessionID == interrupt.SessionID && request.TurnID == interrupt.TurnID {
					e.config.ConfirmationStore.Cancel(interrupt.SessionID, interrupt.TurnID)
					delete(pendingConfirmations, id)
				}
			}
			kept := deferredApprovals[:0]
			for _, deferred := range deferredApprovals {
				if deferred.request.SessionID != interrupt.SessionID || deferred.request.TurnID != interrupt.TurnID {
					kept = append(kept, deferred)
				}
			}
			deferredApprovals = kept
		case request, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			request = cloneRequest(request)
			admission := e.policy.currentSnapshot()
			confirmationDescription, confirmationErr, confirmationRegistered := e.registry.confirmationFor(request.Call.Name, request.Call.Arguments)
			if !confirmationRegistered {
				pending = append(pending, e.newJob(ctx, request, admission))
				continue
			}
			if confirmationErr != nil {
				e.sendResult(ctx, results, failedResult(request, confirmationErr))
				continue
			}
			if confirmationDescription.Message != "" {
				if err := confirmation.ValidateDescription(confirmationDescription); err != nil {
					e.sendResult(ctx, results, failedResult(request, err))
					continue
				}
			}
			description, err, registered := e.registry.permissionFor(request.Call.Name, request.Call.Arguments, admission.policy)
			if !registered {
				job, ready, confirmErr := e.admitConfirmation(ctx, request, admission, confirmationDescription, pendingConfirmations, expiredConfirmations)
				if confirmErr != nil {
					e.sendResult(ctx, results, confirmationAdmissionResult(request, confirmErr))
				} else if ready {
					pending = append(pending, job)
				}
				continue
			}
			if err != nil {
				e.sendResult(ctx, results, failedResult(request, err))
				continue
			}
			var deferredPermission *permission.Description
			if registered && e.config.PermissionEnabled && len(description.Actions) != 0 {
				if description.Risk == "" {
					description.Risk = permission.RiskMedium
				}
				evaluation := permission.Request{
					SessionID: request.SessionID, TurnID: request.TurnID,
					CallID: request.Call.CallID, ToolName: request.Call.Name,
					Actions: description.Actions, Risk: description.Risk,
					Details: description.Details, Reason: description.Reason,
				}
				outcome := permission.Evaluate(evaluation, admission.policy)
				if hasGrant(grants, evaluation, e.config.ProjectID) {
					outcome = permission.OutcomeAllow
				}
				if e.config.NonInteractive && outcome == permission.OutcomeAsk {
					outcome = permission.OutcomeDeny
				}
				if outcome == permission.OutcomeDeny {
					e.sendResult(ctx, results, deniedResult(request))
					continue
				}
				if outcome == permission.OutcomeAsk {
					value := description
					value.Actions = append([]permission.Action(nil), description.Actions...)
					deferredPermission = &value
				}
			}
			if deferredPermission == nil && confirmationDescription.Message == "" {
				pending = append(pending, e.newJob(ctx, request, admission))
				continue
			}
			deferredApprovals = append(deferredApprovals, deferredApproval{
				request: request, admission: admission,
				permission: deferredPermission, confirm: confirmationDescription,
			})
		case jobChannel <- next:
			pending = pending[1:]
		}
	}

	close(jobs)
	workers.Wait()
	return nil
}

func (e *Executor) admitDeferredApproval(
	ctx context.Context,
	deferred deferredApproval,
	grants []permissionGrant,
	permissionTransportOpen bool,
	confirmationTransportOpen bool,
	pendingPermissions map[permission.ID]pendingPermission,
	pendingConfirmations map[confirmation.ID]pendingConfirmation,
	expiredPermissions chan<- permission.ID,
	expiredConfirmations chan<- confirmation.ID,
	results chan<- agentruntime.ToolResultEnvelope,
) (workerJob, bool) {
	if deferred.permission != nil {
		description := *deferred.permission
		now := e.config.Now().UTC()
		prompt := permission.Request{
			SessionID: deferred.request.SessionID, TurnID: deferred.request.TurnID,
			CallID: deferred.request.Call.CallID, ToolName: deferred.request.Call.Name,
			Actions: append([]permission.Action(nil), description.Actions...),
			Risk:    description.Risk, Details: description.Details, Reason: description.Reason,
			CreatedAt: now,
		}
		outcome := permission.Evaluate(prompt, deferred.admission.policy)
		if hasGrant(grants, prompt, e.config.ProjectID) {
			outcome = permission.OutcomeAllow
		}
		if e.config.NonInteractive && outcome == permission.OutcomeAsk {
			outcome = permission.OutcomeDeny
		}
		switch outcome {
		case permission.OutcomeDeny:
			e.sendResult(ctx, results, deniedResult(deferred.request))
			return workerJob{}, false
		case permission.OutcomeAsk:
			if !permissionTransportOpen {
				e.sendResult(ctx, results, deniedResult(deferred.request))
				return workerJob{}, false
			}
			id, err := permission.NewID()
			if err != nil {
				e.sendResult(ctx, results, deniedResult(deferred.request))
				return workerJob{}, false
			}
			prompt.ID = id
			if e.config.PermissionTimeout > 0 {
				expiry := now.Add(e.config.PermissionTimeout)
				prompt.ExpiresAt = &expiry
			}
			if err := e.config.Store.Create(prompt); err != nil {
				e.sendResult(ctx, results, deniedResult(deferred.request))
				return workerJob{}, false
			}
			if !safeSendPermission(ctx, e.config.PermissionRequests, prompt) {
				e.config.Store.Cancel(deferred.request.SessionID, deferred.request.TurnID)
				e.sendResult(ctx, results, deniedResult(deferred.request))
				return workerJob{}, false
			}
			pendingPermissions[id] = pendingPermission{
				request: deferred.request, admission: deferred.admission,
				prompt: clonePermissionRequest(prompt), confirm: deferred.confirm,
			}
			if prompt.ExpiresAt != nil {
				go e.expireAfter(ctx, id, *prompt.ExpiresAt, expiredPermissions)
			}
			return workerJob{}, false
		}
	}

	if deferred.confirm.Message != "" && e.config.ConfirmationEnabled && !confirmationTransportOpen {
		e.sendResult(ctx, results, declinedResult(deferred.request, "confirmation channel closed"))
		return workerJob{}, false
	}
	job, ready, err := e.admitConfirmation(
		ctx,
		deferred.request,
		deferred.admission,
		deferred.confirm,
		pendingConfirmations,
		expiredConfirmations,
	)
	if err != nil {
		e.sendResult(ctx, results, confirmationAdmissionResult(deferred.request, err))
		return workerJob{}, false
	}
	return job, ready
}

func approvalPendingForSession(
	pendingPermissions map[permission.ID]pendingPermission,
	pendingConfirmations map[confirmation.ID]pendingConfirmation,
	sessionID string,
) bool {
	for _, pending := range pendingPermissions {
		if pending.request.SessionID == sessionID {
			return true
		}
	}
	for _, pending := range pendingConfirmations {
		if pending.request.SessionID == sessionID {
			return true
		}
	}
	return false
}

func safeSendPermission(ctx context.Context, requests chan<- permission.Request, request permission.Request) (sent bool) {
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()
	select {
	case requests <- request:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *Executor) admitConfirmation(ctx context.Context, request agentruntime.ToolRequest, admission policySnapshot, description confirmation.Description, pending map[confirmation.ID]pendingConfirmation, expired chan<- confirmation.ID) (workerJob, bool, error) {
	if description.Message == "" {
		return e.newJob(ctx, request, admission), true, nil
	}
	if !e.config.ConfirmationEnabled {
		return workerJob{}, false, errors.New("tool confirmation transport is not enabled")
	}
	if e.config.NonInteractive {
		return workerJob{}, false, errConfirmationDeclined
	}
	id, err := confirmation.NewID()
	if err != nil {
		return workerJob{}, false, err
	}
	now := e.config.Now().UTC()
	prompt := confirmation.Request{
		ID: id, SessionID: request.SessionID, TurnID: request.TurnID,
		CallID: request.Call.CallID, ToolName: request.Call.Name,
		Title: description.Title, Message: description.Message, Details: description.Details,
		CreatedAt: now,
	}
	if e.config.ConfirmationTimeout > 0 {
		expiresAt := now.Add(e.config.ConfirmationTimeout)
		prompt.ExpiresAt = &expiresAt
	}
	if err := e.config.ConfirmationStore.Create(prompt); err != nil {
		return workerJob{}, false, err
	}
	if !safeSendConfirmation(ctx, e.config.ConfirmationRequests, prompt) {
		e.config.ConfirmationStore.Cancel(request.SessionID, request.TurnID)
		return workerJob{}, false, confirmation.ErrClosed
	}
	pending[id] = pendingConfirmation{request: request, admission: admission, prompt: cloneConfirmationRequest(prompt)}
	if prompt.ExpiresAt != nil {
		go e.expireConfirmationAfter(ctx, id, *prompt.ExpiresAt, expired)
	}
	return workerJob{}, false, nil
}

func safeSendConfirmation(ctx context.Context, requests chan<- confirmation.Request, request confirmation.Request) (sent bool) {
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()
	select {
	case requests <- request:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *Executor) expireConfirmationAfter(ctx context.Context, id confirmation.ID, expiry time.Time, expired chan<- confirmation.ID) {
	delay := expiry.Sub(e.config.Now())
	if delay < 0 {
		delay = 0
	}
	select {
	case <-ctx.Done():
		return
	case <-e.config.After(delay):
		select {
		case expired <- id:
		case <-ctx.Done():
		}
	}
}

func cloneConfirmationRequest(request confirmation.Request) confirmation.Request {
	clone := request
	if request.ExpiresAt != nil {
		expiresAt := *request.ExpiresAt
		clone.ExpiresAt = &expiresAt
	}
	return clone
}

func (e *Executor) expireAfter(ctx context.Context, id permission.ID, expiry time.Time, expired chan<- permission.ID) {
	delay := expiry.Sub(e.config.Now())
	if delay < 0 {
		delay = 0
	}
	select {
	case <-ctx.Done():
		return
	case <-e.config.After(delay):
		select {
		case expired <- id:
		case <-ctx.Done():
		}
	}
}

type permissionGrant struct {
	scope     permission.DecisionType
	sessionID string
	projectID string
	actions   []permission.Action
}

func hasGrant(grants []permissionGrant, request permission.Request, projectID string) bool {
	for _, grant := range grants {
		if grant.scope == permission.AllowSession && grant.sessionID != request.SessionID {
			continue
		}
		if grant.scope == permission.AllowProject && (projectID == "" || grant.projectID != projectID) {
			continue
		}
		if sameActions(grant.actions, request.Actions) {
			return true
		}
	}
	return false
}
func sameActions(a, b []permission.Action) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func clonePermissionRequest(request permission.Request) permission.Request {
	clone := request
	clone.Actions = append([]permission.Action(nil), request.Actions...)
	if request.ExpiresAt != nil {
		expiresAt := *request.ExpiresAt
		clone.ExpiresAt = &expiresAt
	}
	return clone
}

func (e *Executor) sendResult(ctx context.Context, results chan<- agentruntime.ToolResultEnvelope, result agentruntime.ToolResultEnvelope) {
	select {
	case results <- result:
	case <-ctx.Done():
	}
}
func deniedResult(request agentruntime.ToolRequest) agentruntime.ToolResultEnvelope {
	return agentruntime.ToolResultEnvelope{SessionID: request.SessionID, TurnID: request.TurnID, Result: agentruntime.ToolResult{CallID: request.Call.CallID, Name: request.Call.Name, Status: agentruntime.ToolResultDenied, Error: "permission denied"}}
}
func declinedResult(request agentruntime.ToolRequest, reason string) agentruntime.ToolResultEnvelope {
	return agentruntime.ToolResultEnvelope{SessionID: request.SessionID, TurnID: request.TurnID, Result: agentruntime.ToolResult{CallID: request.Call.CallID, Name: request.Call.Name, Status: agentruntime.ToolResultDeclined, Error: reason}}
}
func confirmationAdmissionResult(request agentruntime.ToolRequest, err error) agentruntime.ToolResultEnvelope {
	if errors.Is(err, errConfirmationDeclined) {
		return declinedResult(request, "confirmation unavailable in non-interactive mode")
	}
	return failedResult(request, err)
}
func failedResult(request agentruntime.ToolRequest, err error) agentruntime.ToolResultEnvelope {
	return agentruntime.ToolResultEnvelope{SessionID: request.SessionID, TurnID: request.TurnID, Result: agentruntime.ToolResult{CallID: request.Call.CallID, Name: request.Call.Name, Status: agentruntime.ToolResultFailed, Error: err.Error()}}
}
func (e *Executor) denyPendingPermissions(ctx context.Context, p map[permission.ID]pendingPermission, results chan<- agentruntime.ToolResultEnvelope) {
	for id, pending := range p {
		delete(p, id)
		request := pending.request
		e.config.Store.Cancel(request.SessionID, request.TurnID)
		e.sendResult(ctx, results, deniedResult(request))
	}
}
func (e *Executor) cancelPendingPermissions(p map[permission.ID]pendingPermission, results chan<- agentruntime.ToolResultEnvelope) {
	for id := range p {
		delete(p, id)
	}
}

func (e *Executor) declinePendingConfirmations(ctx context.Context, pending map[confirmation.ID]pendingConfirmation, results chan<- agentruntime.ToolResultEnvelope, reason string) {
	for id, item := range pending {
		delete(pending, id)
		e.config.ConfirmationStore.Cancel(item.request.SessionID, item.request.TurnID)
		e.sendResult(ctx, results, declinedResult(item.request, reason))
	}
}

func (e *Executor) cancelPendingConfirmations(pending map[confirmation.ID]pendingConfirmation) {
	for id, item := range pending {
		delete(pending, id)
		e.config.ConfirmationStore.Cancel(item.request.SessionID, item.request.TurnID)
	}
}

func (e *Executor) shutdown(ctx context.Context, jobs chan workerJob, workers *sync.WaitGroup) {
	cause := context.Cause(ctx)
	if cause == nil {
		cause = context.Canceled
	}
	e.cancelAll(cause)
	close(jobs)
	workers.Wait()
}

func (e *Executor) newJob(root context.Context, request agentruntime.ToolRequest, admission policySnapshot) workerJob {
	request = cloneRequest(request)
	admission = policySnapshot{policy: clonePolicyValue(admission.policy), epoch: admission.epoch}
	callContext := WithPermissionPolicy(root, admission.policy)
	callContext = WithInvocation(callContext, Invocation{
		SessionID: request.SessionID,
		TurnID:    request.TurnID,
		CallID:    request.Call.CallID,
		ToolName:  request.Call.Name,
	})
	callContext, cancel := context.WithCancelCause(callContext)
	active := &activeCall{cancel: cancel}
	key := requestKey(request)

	e.mu.Lock()
	calls := e.active[key]
	if calls == nil {
		calls = make(map[*activeCall]struct{})
		e.active[key] = calls
	}
	calls[active] = struct{}{}
	e.mu.Unlock()

	return workerJob{request: request, admission: admission, ctx: callContext, active: active}
}

func (e *Executor) interrupt(interrupt agentruntime.ToolInterrupt) {
	cause := errors.New(interrupt.Reason)
	if interrupt.Reason == "" {
		cause = context.Canceled
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, callID := range interrupt.CallIDs {
		key := callKey{sessionID: interrupt.SessionID, turnID: interrupt.TurnID, callID: callID}
		for call := range e.active[key] {
			call.cancel(cause)
		}
	}
}

func (e *Executor) cancelAll(cause error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, calls := range e.active {
		for call := range calls {
			call.cancel(cause)
		}
	}
}

func (e *Executor) remove(request agentruntime.ToolRequest, active *activeCall) {
	key := requestKey(request)
	e.mu.Lock()
	defer e.mu.Unlock()
	calls := e.active[key]
	delete(calls, active)
	if len(calls) == 0 {
		delete(e.active, key)
	}
}

func requestKey(request agentruntime.ToolRequest) callKey {
	return callKey{sessionID: request.SessionID, turnID: request.TurnID, callID: request.Call.CallID}
}
