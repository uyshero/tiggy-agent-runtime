package agent

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

type contextPreparation struct {
	messages []message.Message
	err      *RunError
	reason   string
	canceled bool
}

func (a *Agent) prepareContext(
	ctx context.Context,
	target ModelTarget,
	messages []message.Message,
	tools []model.ToolDefinition,
	emit *emitter,
) contextPreparation {
	source := message.CloneAll(messages)
	if target.ContextPlanner == nil {
		return contextPreparation{messages: source}
	}
	plan, failure := a.planContext(ctx, target, source, tools, emit, "")
	if failure.err != nil {
		return failure
	}
	if plan.Fits {
		return contextPreparation{messages: source}
	}
	if target.ContextCompressor == nil {
		return contextPreparation{err: contextOverflowError(plan), reason: "context_overflow"}
	}
	return a.compressContext(ctx, target, source, tools, plan, emit)
}

func (a *Agent) compressContext(
	ctx context.Context,
	target ModelTarget,
	messages []message.Message,
	tools []model.ToolDefinition,
	plan contextmanager.Plan,
	emit *emitter,
) contextPreparation {
	request, requestErr := contextmanager.NewCompressionRequest(target.Identity, messages, plan)
	candidateGroups := len(request.Groups)
	started, emitErr := emit.emit(event.CompressionStarted, "", event.CompressionPayload{
		Model: target.Identity, BeforeInputTokens: plan.InputTokens, TargetInputTokens: plan.AvailableInputTokens,
		OverflowTokens: plan.OverflowTokens, CandidateGroups: candidateGroups,
	})
	if emitErr != nil {
		runtimeError := &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emitErr}
		_, _ = emit.emit(event.CompressionFailed, started.ID, compressionFailurePayload(target.Identity, plan, candidateGroups, "failed", runtimeError))
		return contextPreparation{err: runtimeError, reason: "event_sink_failed"}
	}
	if requestErr != nil {
		runtimeError := &RunError{
			Class: ErrorLimit, Code: "context_compression_unavailable",
			SafeMessage: "The overflowing context has no history that can be compressed safely.", Cause: requestErr,
		}
		_, completedErr := emit.emit(event.CompressionFailed, started.ID, compressionFailurePayload(target.Identity, plan, candidateGroups, "failed", runtimeError))
		if completedErr != nil {
			return contextPreparation{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
		}
		return contextPreparation{err: runtimeError, reason: "context_overflow"}
	}

	decision, compressionErr := callCompressor(ctx, target.ContextCompressor, request)
	if ctxErr := ctx.Err(); ctxErr != nil {
		runtimeError := &RunError{Class: ErrorCanceled, Code: "context_compression_canceled", SafeMessage: "Context compression was canceled.", Cause: ctxErr}
		_, _ = emit.emit(event.CompressionFailed, started.ID, compressionFailurePayload(target.Identity, plan, candidateGroups, "canceled", runtimeError))
		return contextPreparation{err: runtimeError, reason: "context_canceled", canceled: true}
	}
	if compressionErr != nil {
		runtimeError := &RunError{Class: ErrorInternal, Code: "context_compression_failed", SafeMessage: "The Agent could not compress its model context.", Cause: compressionErr}
		_, completedErr := emit.emit(event.CompressionFailed, started.ID, compressionFailurePayload(target.Identity, plan, candidateGroups, "failed", runtimeError))
		if completedErr != nil {
			return contextPreparation{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
		}
		return contextPreparation{err: runtimeError, reason: "context_compression_failed"}
	}
	compressed, applyErr := contextmanager.ApplyCompression(messages, request, decision, a.config.IDs("msg"))
	if applyErr != nil {
		runtimeError := &RunError{Class: ErrorInternal, Code: "invalid_compression_result", SafeMessage: "The context compressor returned an invalid result.", Cause: applyErr}
		_, completedErr := emit.emit(event.CompressionFailed, started.ID, compressionFailurePayload(target.Identity, plan, candidateGroups, "failed", runtimeError))
		if completedErr != nil {
			return contextPreparation{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
		}
		return contextPreparation{err: runtimeError, reason: "context_compression_failed"}
	}

	compressedPlan, planFailure := a.planContext(ctx, target, compressed.Messages, tools, emit, started.ID)
	if planFailure.err != nil {
		payload := compressionFailurePayload(target.Identity, plan, candidateGroups, "failed", planFailure.err)
		if planFailure.canceled {
			payload.Status = "canceled"
		}
		payload.StrategyName = compressed.Strategy.Name
		payload.StrategyVersion = compressed.Strategy.Version
		payload.CoveredGroups = compressed.CoveredGroups
		payload.CoveredMessageIDs = append([]string(nil), compressed.CoveredMessageIDs...)
		payload.SummaryMessageID = compressed.SummaryMessageID
		_, _ = emit.emit(event.CompressionFailed, started.ID, payload)
		return planFailure
	}
	payload := event.CompressionPayload{
		Model: target.Identity, Status: "completed", StrategyName: compressed.Strategy.Name,
		StrategyVersion: compressed.Strategy.Version, BeforeInputTokens: plan.InputTokens,
		AfterInputTokens: compressedPlan.InputTokens, TargetInputTokens: plan.AvailableInputTokens,
		OverflowTokens: plan.OverflowTokens, CandidateGroups: candidateGroups, CoveredGroups: compressed.CoveredGroups,
		CoveredMessageIDs: append([]string(nil), compressed.CoveredMessageIDs...), SummaryMessageID: compressed.SummaryMessageID,
	}
	if !compressedPlan.Fits {
		payload.Status = "insufficient"
		payload.Fallback = "fail_closed"
		_, completedErr := emit.emit(event.CompressionCompleted, started.ID, payload)
		if completedErr != nil {
			return contextPreparation{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
		}
		return contextPreparation{err: contextOverflowError(compressedPlan), reason: "context_overflow"}
	}
	payload.Fallback = "none"
	_, completedErr := emit.emit(event.CompressionCompleted, started.ID, payload)
	if completedErr != nil {
		return contextPreparation{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
	}
	return contextPreparation{messages: compressed.Messages}
}

func (a *Agent) planContext(
	ctx context.Context,
	target ModelTarget,
	messages []message.Message,
	tools []model.ToolDefinition,
	emit *emitter,
	parentID string,
) (contextmanager.Plan, contextPreparation) {
	request := contextmanager.Request{
		Model: target.Identity, Messages: message.CloneAll(messages), Tools: cloneToolDefinitions(tools),
		OutputReserveTokens: int64(a.config.Limits.MaxOutputTokensPerCall),
	}
	started, emitErr := emit.emit(event.ContextPrepareStarted, parentID, event.ContextPreparePayload{
		Model: target.Identity, OutputReserveTokens: request.OutputReserveTokens,
	})
	if emitErr != nil {
		runtimeError := eventSinkRunError(emitErr)
		_, _ = emit.emit(event.ContextPrepared, started.ID, event.ContextPreparePayload{
			Model: target.Identity, Status: "failed", OutputReserveTokens: request.OutputReserveTokens, Error: eventError(runtimeError),
		})
		return contextmanager.Plan{}, contextPreparation{err: runtimeError, reason: "event_sink_failed"}
	}
	plan, planErr := callContextPlanner(ctx, target.ContextPlanner, request)
	if ctxErr := ctx.Err(); ctxErr != nil {
		runtimeError := &RunError{Class: ErrorCanceled, Code: "context_prepare_canceled", SafeMessage: "Context preparation was canceled.", Cause: ctxErr}
		_, _ = emit.emit(event.ContextPrepared, started.ID, event.ContextPreparePayload{
			Model: target.Identity, Status: "canceled", OutputReserveTokens: request.OutputReserveTokens, Error: eventError(runtimeError),
		})
		return contextmanager.Plan{}, contextPreparation{err: runtimeError, reason: "context_canceled", canceled: true}
	}
	if planErr != nil {
		runtimeError := &RunError{Class: ErrorInternal, Code: "context_planner_failed", SafeMessage: "The Agent could not plan its model context.", Cause: planErr}
		_, completedErr := emit.emit(event.ContextPrepared, started.ID, event.ContextPreparePayload{
			Model: target.Identity, Status: "failed", OutputReserveTokens: request.OutputReserveTokens, Error: eventError(runtimeError),
		})
		if completedErr != nil {
			return contextmanager.Plan{}, contextPreparation{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
		}
		return contextmanager.Plan{}, contextPreparation{err: runtimeError, reason: "context_planner_failed"}
	}
	if err := validateContextPlan(plan, request); err != nil {
		runtimeError := &RunError{Class: ErrorInternal, Code: "invalid_context_plan", SafeMessage: "The context planner returned an invalid plan.", Cause: err}
		payload := contextEventPayload(plan, "failed", eventError(runtimeError))
		payload.Model = target.Identity
		payload.OutputReserveTokens = request.OutputReserveTokens
		_, completedErr := emit.emit(event.ContextPrepared, started.ID, payload)
		if completedErr != nil {
			return contextmanager.Plan{}, contextPreparation{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
		}
		return contextmanager.Plan{}, contextPreparation{err: runtimeError, reason: "context_planner_failed"}
	}
	status := "ready"
	var payloadError *event.ErrorPayload
	if !plan.Fits {
		status = "overflow"
		payloadError = eventError(contextOverflowError(plan))
	}
	_, completedErr := emit.emit(event.ContextPrepared, started.ID, contextEventPayload(plan, status, payloadError))
	if completedErr != nil {
		return contextmanager.Plan{}, contextPreparation{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
	}
	return plan, contextPreparation{}
}

func callContextPlanner(ctx context.Context, planner contextmanager.Planner, request contextmanager.Request) (plan contextmanager.Plan, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("context planner panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return planner.Plan(ctx, contextmanager.Request{
		Model: request.Model, Messages: message.CloneAll(request.Messages), Tools: cloneToolDefinitions(request.Tools),
		OutputReserveTokens: request.OutputReserveTokens,
	})
}

func callCompressor(ctx context.Context, compressor contextmanager.Compressor, request contextmanager.CompressionRequest) (decision contextmanager.CompressionDecision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("context compressor panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return compressor.Compress(ctx, request.Clone())
}

func validateContextPlan(plan contextmanager.Plan, request contextmanager.Request) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.Model != request.Model {
		return fmt.Errorf("context plan changed model identity")
	}
	if plan.OutputReserveTokens != request.OutputReserveTokens {
		return fmt.Errorf("context plan changed output reserve")
	}
	return nil
}

func contextOverflowError(plan contextmanager.Plan) *RunError {
	return &RunError{
		Class: ErrorLimit, Code: "context_overflow", SafeMessage: "The model context exceeds its configured token budget.",
		Cause: fmt.Errorf("context exceeds available input by %d tokens", plan.OverflowTokens),
	}
}

func eventSinkRunError(cause error) *RunError {
	return &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: cause}
}

func compressionFailurePayload(identity model.Identity, plan contextmanager.Plan, candidateGroups int, status string, runtimeError *RunError) event.CompressionPayload {
	return event.CompressionPayload{
		Model: identity, Status: status, BeforeInputTokens: plan.InputTokens,
		TargetInputTokens: plan.AvailableInputTokens, OverflowTokens: plan.OverflowTokens,
		CandidateGroups: candidateGroups, Fallback: "fail_closed", Error: eventError(runtimeError),
	}
}

func contextEventPayload(plan contextmanager.Plan, status string, payloadError *event.ErrorPayload) event.ContextPreparePayload {
	return event.ContextPreparePayload{
		Model: plan.Model, Status: status, CountSource: string(plan.CountSource),
		ContextWindowTokens: plan.ContextWindowTokens, InputTokens: plan.InputTokens,
		OutputReserveTokens: plan.OutputReserveTokens, SafetyMarginTokens: plan.SafetyMarginTokens,
		RequiredTokens:       plan.RequiredTokens,
		AvailableInputTokens: plan.AvailableInputTokens, RemainingInputTokens: plan.RemainingInputTokens,
		OverflowTokens: plan.OverflowTokens, UtilizationBasisPoints: plan.UtilizationBasisPoints,
		Pressure: string(plan.Pressure), Fits: plan.Fits, Error: payloadError,
	}
}
