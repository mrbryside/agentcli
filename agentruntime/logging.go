package agentruntime

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/mrbryside/agentcli/provider"
)

const maximumRuntimeLogValueRunes = 4096

func (runtime *Runtime) logEvent(ctx context.Context, run *Run, event AgentEvent) {
	if runtime == nil || runtime.logger == nil || run == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("session_id", event.SessionID),
		slog.String("turn_id", event.TurnID),
		slog.Uint64("sequence", event.Sequence),
	}
	switch event.Type {
	case RunStarted:
		runtime.logger.LogAttrs(ctx, slog.LevelInfo, "agent turn started", attrs...)
	case ProviderEventReceived:
		if event.ProviderEvent.Type != provider.StreamCompleted {
			return
		}
		result, _ := terminalProviderResult(event.ProviderEvent)
		content := result.Content
		if content == "" {
			content = event.ProviderEvent.Content
		}
		attrs = append(attrs,
			slog.Int("provider_step", run.providerSteps()),
			slog.String("finish_reason", event.ProviderEvent.FinishReason),
			slog.Int("tool_calls", len(result.CompletedTools)),
		)
		if content != "" {
			attrs = append(attrs, slog.String("content", content))
		}
		runtime.logger.LogAttrs(ctx, slog.LevelDebug, "agent content completed", attrs...)
	case ToolCallRequested:
		if event.ToolRequest == nil {
			return
		}
		call := event.ToolRequest.Call
		attrs = append(attrs,
			slog.String("call_id", call.CallID),
			slog.String("tool", call.Name),
		)
		if len(call.Arguments) != 0 {
			attrs = append(attrs, slog.String("parameters", formatRuntimeLogValue(call.Arguments)))
		}
		runtime.logger.LogAttrs(ctx, slog.LevelDebug, "agent tool call requested", attrs...)
	case ToolResultReceived:
		if event.ToolResult == nil {
			return
		}
		result := event.ToolResult.Result
		attrs = append(attrs,
			slog.String("call_id", result.CallID),
			slog.String("tool", result.Name),
			slog.String("status", string(result.Status)),
		)
		if len(result.Output) != 0 {
			attrs = append(attrs, slog.String("result", formatRuntimeLogValue(result.Output)))
		}
		if result.Error != "" {
			attrs = append(attrs, slog.String("error", result.Error))
		}
		runtime.logger.LogAttrs(ctx, slog.LevelDebug, "agent tool executed", attrs...)
	case CompactionStarted:
		runtime.logger.LogAttrs(ctx, slog.LevelDebug, "agent compaction started", attrs...)
	case CompactionCompleted:
		runtime.logger.LogAttrs(ctx, slog.LevelDebug, "agent compaction completed", attrs...)
	case CompactionFailed:
		if event.Error != nil {
			attrs = append(attrs, slog.Any("error", event.Error))
		}
		runtime.logger.LogAttrs(ctx, slog.LevelError, "agent compaction failed", attrs...)
	case RunCompleted:
		attrs = append(attrs,
			slog.Int("provider_steps", run.providerSteps()),
			slog.Int("completion_repairs", run.CompletionRepairCount()),
			slog.Int("output_guard_retries", run.OutputGuardRetryCount()),
		)
		runtime.logger.LogAttrs(ctx, slog.LevelInfo, "agent turn completed", attrs...)
	case RunFailed:
		if event.Error != nil {
			attrs = append(attrs, slog.Any("error", event.Error))
		}
		runtime.logger.LogAttrs(ctx, slog.LevelError, "agent turn failed", attrs...)
	case AgentInterrupted:
		if event.Reason != "" {
			attrs = append(attrs, slog.String("reason", event.Reason))
		}
		runtime.logger.LogAttrs(ctx, slog.LevelWarn, "agent turn interrupted", attrs...)
	}
}

func formatRuntimeLogValue(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err == nil {
		decoded = redactRuntimeLogValue(decoded)
		if encoded, err := json.Marshal(decoded); err == nil {
			return truncateRuntimeLogValue(string(encoded))
		}
	}
	return truncateRuntimeLogValue(string(value))
}

func redactRuntimeLogValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			lowerKey := strings.ToLower(key)
			if strings.Contains(lowerKey, "token") ||
				strings.Contains(lowerKey, "secret") ||
				strings.Contains(lowerKey, "password") ||
				strings.Contains(lowerKey, "authorization") ||
				strings.Contains(lowerKey, "api_key") {
				typed[key] = "[redacted]"
				continue
			}
			typed[key] = redactRuntimeLogValue(item)
		}
	case []any:
		for index, item := range typed {
			typed[index] = redactRuntimeLogValue(item)
		}
	}
	return value
}

func truncateRuntimeLogValue(value string) string {
	if utf8.RuneCountInString(value) <= maximumRuntimeLogValueRunes {
		return value
	}
	return string([]rune(value)[:maximumRuntimeLogValueRunes]) + "…[truncated]"
}

func (runtime *Runtime) logRepairRequested(
	ctx context.Context,
	run *Run,
	repairType string,
	attempt int,
	providerSteps int,
	toolAllowlist []string,
) {
	if runtime == nil || runtime.logger == nil || run == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("session_id", run.sessionID),
		slog.String("turn_id", run.turnID),
		slog.String("repair_type", repairType),
		slog.Int("attempt", attempt),
		slog.Int("provider_steps", providerSteps),
	}
	if toolAllowlist != nil {
		attrs = append(attrs, slog.Any("tool_allowlist", append([]string(nil), toolAllowlist...)))
	}
	runtime.logger.LogAttrs(ctx, slog.LevelInfo, "agent repair requested", attrs...)
}

func (runtime *Runtime) logRepairFailed(ctx context.Context, run *Run, repairType string, err error) {
	if runtime == nil || runtime.logger == nil || run == nil {
		return
	}
	runtime.logger.ErrorContext(ctx, "agent repair failed",
		"session_id", run.sessionID,
		"turn_id", run.turnID,
		"repair_type", repairType,
		"provider_steps", run.providerSteps(),
		"error", err,
	)
}
