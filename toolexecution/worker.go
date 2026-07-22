package toolexecution

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mrbryside/agentcli/agentruntime"
)

func (e *Executor) worker(root context.Context, jobs <-chan workerJob, results chan<- agentruntime.ToolResultEnvelope, workers *sync.WaitGroup) {
	defer workers.Done()
	for job := range jobs {
		result := policyChangedResult(job.request)
		e.policy.executeIfCurrent(job.admission, func() {
			result = e.execute(job.ctx, job.request)
		})
		e.remove(job.request, job.active)

		select {
		case results <- result:
		case <-root.Done():
		}
	}
}

func policyChangedResult(request agentruntime.ToolRequest) agentruntime.ToolResultEnvelope {
	return agentruntime.ToolResultEnvelope{
		SessionID: request.SessionID,
		TurnID:    request.TurnID,
		Result: agentruntime.ToolResult{
			CallID: request.Call.CallID,
			Name:   request.Call.Name,
			Status: agentruntime.ToolResultFailed,
			Error:  "permission policy changed before execution; retry the request",
		},
	}
}

func (e *Executor) execute(ctx context.Context, request agentruntime.ToolRequest) (result agentruntime.ToolResultEnvelope) {
	result = agentruntime.ToolResultEnvelope{
		SessionID: request.SessionID,
		TurnID:    request.TurnID,
		Result: agentruntime.ToolResult{
			CallID: request.Call.CallID,
			Name:   request.Call.Name,
		},
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			result.Result.Status = agentruntime.ToolResultFailed
			result.Result.Output = nil
			result.Result.Error = fmt.Sprintf("tool handler panicked: %v", recovered)
		}
		if ctx.Err() != nil {
			result.Result.Status = agentruntime.ToolResultInterrupted
			result.Result.Output = nil
			result.Result.Error = contextError(ctx)
		}
	}()

	if ctx.Err() != nil {
		return result
	}
	handler, ok := e.registry.lookup(request.Call.Name)
	if !ok {
		result.Result.Status = agentruntime.ToolResultFailed
		result.Result.Error = fmt.Sprintf("tool %q is not registered", request.Call.Name)
		return result
	}

	output, err := handler(ctx, cloneRawJSON(request.Call.Arguments))
	if err != nil {
		result.Result.Status = agentruntime.ToolResultFailed
		result.Result.Error = err.Error()
		return result
	}
	result.Result.Status = agentruntime.ToolResultSucceeded
	result.Result.Output = cloneRawJSON(output)
	return result
}

func contextError(ctx context.Context) string {
	if cause := context.Cause(ctx); cause != nil {
		return cause.Error()
	}
	return context.Canceled.Error()
}

func cloneRequest(request agentruntime.ToolRequest) agentruntime.ToolRequest {
	clone := request
	clone.Call.Arguments = cloneRawJSON(request.Call.Arguments)
	return clone
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	clone := make(json.RawMessage, len(raw))
	copy(clone, raw)
	return clone
}
