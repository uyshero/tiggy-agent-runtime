package observe

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

// LogOptions 自定义事件严重级别。Payload 正文永不落日志；
// LogSink 只输出选定的协议元数据与安全的 ErrorPayload 字段。
type LogOptions struct {
	Level func(event.Event) slog.Level
}

// LogSink 将 slog.Logger 适配为 Runtime Event Sink。
// Handler 错误保留严格 Sink 语义；日志故障不得导致 Run 失败时，用 BestEffort 包装。
func LogSink(logger *slog.Logger, options LogOptions) (event.Sink, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	return func(value event.Event) error {
		if err := value.Validate(); err != nil {
			return err
		}
		level := defaultLogLevel(value.Type)
		if options.Level != nil {
			level = options.Level(value)
		}
		ctx := context.Background()
		handler := logger.Handler()
		if !handler.Enabled(ctx, level) {
			return nil
		}
		record := slog.NewRecord(value.CreatedAt, level, "tiggy runtime event", 0)
		record.AddAttrs(
			slog.String("event.type", string(value.Type)),
			slog.String("event.id", value.ID),
			slog.String("run.id", value.RunID),
			slog.Int64("event.sequence", value.Sequence),
		)
		if value.ParentID != "" {
			record.AddAttrs(slog.String("event.parent_id", value.ParentID))
		}
		attributes, err := logAttributes(value)
		if err != nil {
			return err
		}
		record.AddAttrs(attributes...)
		return handler.Handle(ctx, record)
	}, nil
}

func defaultLogLevel(eventType event.Type) slog.Level {
	switch eventType {
	case event.ModelCallDelta, event.ToolCallDelta,
		event.ModelCallStarted, event.ToolCallStarted, event.ContextPrepareStarted,
		event.CompressionStarted, event.ToolResultOffloadStarted,
		event.MemoryRecallStarted, event.MemoryObserveStarted:
		return slog.LevelDebug
	case event.RunCanceled, event.RetryScheduled, event.FallbackActivated, event.LimitReached,
		event.ApprovalRequired:
		return slog.LevelWarn
	case event.RunFailed, event.RuntimeError, event.CompressionFailed,
		event.ToolResultOffloadFailed, event.MemoryObserveFailed:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func logAttributes(value event.Event) ([]slog.Attr, error) {
	switch value.Type {
	case event.RunStarted:
		payload, err := decode[event.RunStartedPayload](value)
		if err != nil {
			return nil, err
		}
		return []slog.Attr{
			slog.String("agent.name", payload.Agent),
			slog.Int("skill.count", len(payload.Skills)),
			slog.Int("tool_group.count", len(payload.ToolGroups)),
		}, nil
	case event.RunCompleted, event.RunFailed, event.RunCanceled, event.RunSuspended:
		payload, err := decode[event.RunTerminalPayload](value)
		if err != nil {
			return nil, err
		}
		result := []slog.Attr{
			slog.String("run.status", payload.Status),
			slog.String("run.reason", payload.Reason),
			slog.Int("run.iterations", payload.Iterations),
			slog.Int64("usage.input_tokens", payload.Usage.InputTokens),
			slog.Int64("usage.output_tokens", payload.Usage.OutputTokens),
			slog.Int64("usage.reasoning_tokens", payload.Usage.ReasoningTokens),
		}
		return appendErrorAttrs(result, payload.Error), nil
	case event.ApprovalRequired, event.ApprovalResolved:
		payload, err := decode[event.ApprovalPayload](value)
		if err != nil {
			return nil, err
		}
		return []slog.Attr{
			slog.String("approval.id", payload.ApprovalID),
			slog.String("approval.status", payload.Status),
			slog.String("suspension.id", payload.SuspensionID),
			slog.String("tool.call_id", payload.CallID),
			slog.String("tool.name", payload.Tool),
		}, nil
	case event.ModelCallStarted, event.ModelCallCompleted:
		payload, err := decode[event.ModelCallPayload](value)
		if err != nil {
			return nil, err
		}
		result := []slog.Attr{
			slog.String("model.call_id", payload.CallID),
			slog.String("model.provider", payload.Model.Provider),
			slog.String("model.name", payload.Model.Model),
			slog.String("model.revision", payload.Model.Revision),
			slog.Int("model.attempt", payload.Attempt),
			slog.String("operation.status", payload.Status),
			slog.String("model.stop_reason", payload.StopReason),
		}
		if payload.Context != nil {
			result = append(result,
				slog.Int("context.projected_message_count", payload.Context.ProjectedMessageCount),
				slog.Int("context.request_message_count", payload.Context.RequestMessageCount),
				slog.Int("context.recalled_memory_messages", payload.Context.RecalledMemoryMessages),
				slog.Int("context.compression_summaries", payload.Context.CompressionSummaries),
				slog.Int64("context.text_bytes", payload.Context.TextBytes),
				slog.Int("context.artifact_blocks", payload.Context.ArtifactBlocks),
				slog.Int("context.tool_definition_count", payload.Context.ToolDefinitionCount),
				slog.Int64("context.tool_definition_bytes", payload.Context.ToolDefinitionBytes),
			)
		}
		return appendErrorAttrs(result, payload.Error), nil
	case event.ModelCallDelta:
		payload, err := decode[event.ModelDeltaPayload](value)
		if err != nil {
			return nil, err
		}
		return []slog.Attr{
			slog.String("model.call_id", payload.CallID),
			slog.String("model.delta_type", string(payload.Delta.Type)),
		}, nil
	case event.ToolCallStarted, event.ToolCallCompleted:
		payload, err := decode[event.ToolCallPayload](value)
		if err != nil {
			return nil, err
		}
		result := []slog.Attr{
			slog.String("tool.call_id", payload.CallID),
			slog.String("tool.name", payload.Name),
			slog.String("operation.status", payload.Status),
			slog.Bool("tool.is_error", payload.IsError),
		}
		return appendErrorAttrs(result, payload.Error), nil
	case event.ToolCallDelta:
		return nil, nil
	case event.ContextPrepareStarted, event.ContextPrepared:
		payload, err := decode[event.ContextPreparePayload](value)
		if err != nil {
			return nil, err
		}
		result := []slog.Attr{
			slog.String("model.provider", payload.Model.Provider),
			slog.String("model.name", payload.Model.Model),
			slog.String("operation.status", payload.Status),
			slog.Int64("context.required_tokens", payload.RequiredTokens),
			slog.Int64("context.available_input_tokens", payload.AvailableInputTokens),
			slog.Int64("context.overflow_tokens", payload.OverflowTokens),
			slog.Int64("context.utilization_basis_points", payload.UtilizationBasisPoints),
			slog.String("context.pressure", payload.Pressure),
		}
		return appendErrorAttrs(result, payload.Error), nil
	case event.CompressionStarted, event.CompressionCompleted, event.CompressionFailed:
		payload, err := decode[event.CompressionPayload](value)
		if err != nil {
			return nil, err
		}
		result := []slog.Attr{
			slog.String("model.provider", payload.Model.Provider),
			slog.String("model.name", payload.Model.Model),
			slog.String("operation.status", payload.Status),
			slog.String("compression.strategy", payload.StrategyName),
			slog.String("compression.strategy_version", payload.StrategyVersion),
			slog.Int64("compression.before_input_tokens", payload.BeforeInputTokens),
			slog.Int64("compression.after_input_tokens", payload.AfterInputTokens),
			slog.Int("compression.covered_groups", payload.CoveredGroups),
		}
		return appendErrorAttrs(result, payload.Error), nil
	case event.ToolResultOffloadStarted, event.ToolResultOffloadCompleted, event.ToolResultOffloadFailed:
		payload, err := decode[event.ToolResultOffloadPayload](value)
		if err != nil {
			return nil, err
		}
		result := []slog.Attr{
			slog.String("tool.call_id", payload.CallID),
			slog.String("tool.name", payload.Name),
			slog.String("operation.status", payload.Status),
			slog.Int64("artifact.source_bytes", payload.SourceBytes),
			slog.String("artifact.media_type", payload.MediaType),
			slog.String("artifact.fallback", payload.Fallback),
		}
		return appendErrorAttrs(result, payload.Error), nil
	case event.MemoryRecallStarted, event.MemoryRecallCompleted,
		event.MemoryObserveStarted, event.MemoryObserveCompleted, event.MemoryObserveFailed:
		payload, err := decode[event.MemoryPayload](value)
		if err != nil {
			return nil, err
		}
		result := []slog.Attr{
			slog.String("memory.namespace", payload.Namespace),
			slog.String("operation.status", payload.Status),
			slog.String("memory.failure_mode", payload.FailureMode),
			slog.Int("memory.item_count", payload.ItemCount),
			slog.Int("memory.expired_item_count", payload.ExpiredItemCount),
			slog.Int64("memory.total_bytes", payload.TotalBytes),
			slog.Int("memory.message_count", payload.MessageCount),
			slog.Int64("memory.observation_bytes", payload.ObservationBytes),
		}
		return appendErrorAttrs(result, payload.Error), nil
	case event.RetryScheduled:
		payload, err := decode[event.RetryScheduledPayload](value)
		if err != nil {
			return nil, err
		}
		result := []slog.Attr{
			slog.String("model.call_id", payload.FailedCallID),
			slog.String("model.provider", payload.Model.Provider),
			slog.String("model.name", payload.Model.Model),
			slog.Int("model.next_attempt", payload.NextAttempt),
			slog.Int64("retry.delay_millis", payload.DelayMillis),
			slog.String("retry.reason", payload.Reason),
		}
		return appendErrorAttrs(result, payload.Error), nil
	case event.FallbackActivated:
		payload, err := decode[event.FallbackActivatedPayload](value)
		if err != nil {
			return nil, err
		}
		result := []slog.Attr{
			slog.String("model.call_id", payload.FailedCallID),
			slog.String("model.from_provider", payload.From.Provider),
			slog.String("model.from_name", payload.From.Model),
			slog.String("model.to_provider", payload.To.Provider),
			slog.String("model.to_name", payload.To.Model),
			slog.Int("model.fallback_index", payload.FallbackIndex),
			slog.String("fallback.reason", payload.Reason),
		}
		return appendErrorAttrs(result, payload.Error), nil
	case event.RuntimeError:
		payload, err := decode[event.ErrorPayload](value)
		if err != nil {
			return nil, err
		}
		return appendErrorAttrs(nil, &payload), nil
	case event.LimitReached:
		return nil, nil
	default:
		// New Event types are protocol-compatible. Preserve their envelope while
		// ignoring payloads whose privacy and cardinality semantics are unknown.
		return nil, nil
	}
}

func appendErrorAttrs(attributes []slog.Attr, payload *event.ErrorPayload) []slog.Attr {
	if payload == nil {
		return attributes
	}
	return append(attributes,
		slog.String("error.class", payload.Class),
		slog.String("error.code", payload.Code),
		slog.String("error.message", payload.Message),
		slog.Bool("error.retryable", payload.Retryable),
	)
}
