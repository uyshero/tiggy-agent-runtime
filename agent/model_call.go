package agent

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// modelGeneration 是一次（可能含重试/降级）模型生成的编排结果。
type modelGeneration struct {
	response  model.Response
	callID    string
	startedID string
	target    model.Identity
	attempt   int
	err       *RunError
	reason    string
	canceled  bool
}

// generateModel 按主模型与 Fallbacks 调用供应商，并应用 FailurePolicy 的重试/降级决策。
func (a *Agent) generateModel(
	ctx context.Context,
	runID string,
	messages []message.Message,
	tools []model.ToolDefinition,
	emit *emitter,
	modelCalls *int,
) modelGeneration {
	targets := make([]ModelTarget, 0, len(a.config.Fallbacks)+1)
	targets = append(targets, ModelTarget{
		Model: a.config.Model, Identity: a.config.ModelIdentity,
		ContextPlanner: a.config.ContextPlanner, ContextCompressor: a.config.ContextCompressor,
	})
	targets = append(targets, a.config.Fallbacks...)
	targetIndex := 0
	attempt := 0
	plannedTargetIndex := -1
	var preparedMessages []message.Message

	for {
		if err := ctx.Err(); err != nil {
			return canceledModelGeneration("model_canceled", err)
		}
		if *modelCalls >= a.config.Limits.MaxModelCalls {
			return modelGeneration{
				err:    &RunError{Class: ErrorLimit, Code: "max_model_calls", SafeMessage: "The Agent reached its model-call limit."},
				reason: "model_call_limit",
			}
		}

		target := targets[targetIndex]
		if plannedTargetIndex != targetIndex {
			preparation := a.prepareContext(ctx, target, messages, tools, emit)
			if preparation.err != nil {
				return modelGeneration{err: preparation.err, reason: preparation.reason, canceled: preparation.canceled}
			}
			preparedMessages = preparation.messages
			plannedTargetIndex = targetIndex
		}
		attempt++
		*modelCalls++
		callID := a.config.IDs("model")
		started, err := emit.emit(event.ModelCallStarted, "", event.ModelCallPayload{
			CallID: callID, Model: target.Identity, Attempt: attempt,
		})
		if err != nil {
			_, _ = emit.emit(event.ModelCallCompleted, started.ID, event.ModelCallPayload{
				CallID: callID, Model: target.Identity, Attempt: attempt, Status: "failed",
				Error: &event.ErrorPayload{Class: string(ErrorEventSink), Code: "event_sink_failed", Message: "The event consumer failed."},
			})
			return eventSinkGeneration(err)
		}

		request := model.Request{
			RunID: runID, CallID: callID, Purpose: model.PurposeAgent, Model: target.Identity,
			Messages: message.CloneAll(preparedMessages), Tools: cloneToolDefinitions(tools),
			MaxOutputTokens: a.config.Limits.MaxOutputTokensPerCall,
		}
		response, modelErr := callModel(ctx, target.Model, request, func(delta model.Delta) error {
			_, err := emit.emit(event.ModelCallDelta, started.ID, event.ModelDeltaPayload{CallID: callID, Delta: delta})
			return err
		})
		if emit.sinkErr != nil {
			_, _ = emit.emit(event.ModelCallCompleted, started.ID, event.ModelCallPayload{
				CallID: callID, Model: target.Identity, Attempt: attempt, Status: "failed",
				Error: &event.ErrorPayload{Class: string(ErrorEventSink), Code: "event_sink_failed", Message: "The event consumer failed."},
			})
			return eventSinkGeneration(emit.sinkErr)
		}
		if modelErr == nil {
			return modelGeneration{
				response: response, callID: callID, startedID: started.ID,
				target: target.Identity, attempt: attempt,
			}
		}

		if ctx.Err() != nil {
			_, _ = emit.emit(event.ModelCallCompleted, started.ID, event.ModelCallPayload{
				CallID: callID, Model: target.Identity, Attempt: attempt, Status: "canceled",
			})
			return canceledModelGeneration("model_canceled", ctx.Err())
		}
		runtimeError := modelRunError(modelErr)
		completed, completedErr := emit.emit(event.ModelCallCompleted, started.ID, event.ModelCallPayload{
			CallID: callID, Model: target.Identity, Attempt: attempt, Status: "failed", Error: eventError(runtimeError),
		})
		if completedErr != nil {
			return eventSinkGeneration(completedErr)
		}

		failure := ModelFailure{
			TargetIndex: targetIndex, TargetCount: len(targets), Attempt: attempt,
			RemainingCalls: a.config.Limits.MaxModelCalls - *modelCalls,
			Error:          providerErrorCopy(modelErr),
		}
		decision, decisionErr := decideModelFailure(a.config.FailurePolicy, failure)
		if decisionErr != nil {
			return modelGeneration{
				err: &RunError{
					Class: ErrorInternal, Code: "model_failure_policy_failed",
					SafeMessage: "The model failure policy failed.", Cause: decisionErr,
				},
				reason: "model_failure_policy_failed",
			}
		}

		switch decision.Action {
		case ModelFailureFail:
			return modelGeneration{err: runtimeError, reason: "model_failed"}
		case ModelFailureRetry:
			if failure.RemainingCalls <= 0 {
				return modelGeneration{
					err:    &RunError{Class: ErrorLimit, Code: "max_model_calls", SafeMessage: "The Agent reached its model-call limit.", Cause: runtimeError},
					reason: "model_call_limit",
				}
			}
			_, retryEmitErr := emit.emit(event.RetryScheduled, completed.ID, event.RetryScheduledPayload{
				FailedCallID: callID, Model: target.Identity, NextAttempt: attempt + 1,
				DelayMillis: decision.Delay.Milliseconds(), Reason: decision.Reason, Error: eventError(runtimeError),
			})
			if retryEmitErr != nil {
				return eventSinkGeneration(retryEmitErr)
			}
			if err := waitForRetry(ctx, decision.Delay); err != nil {
				return canceledModelGeneration("retry_canceled", err)
			}
		case ModelFailureFallback:
			if failure.RemainingCalls <= 0 {
				return modelGeneration{
					err:    &RunError{Class: ErrorLimit, Code: "max_model_calls", SafeMessage: "The Agent reached its model-call limit.", Cause: runtimeError},
					reason: "model_call_limit",
				}
			}
			nextTargetIndex := targetIndex + 1
			if nextTargetIndex >= len(targets) {
				return modelGeneration{
					err: &RunError{
						Class: ErrorInternal, Code: "invalid_fallback_decision",
						SafeMessage: "The model failure policy selected an unavailable fallback.", Cause: runtimeError,
					},
					reason: "model_failure_policy_failed",
				}
			}
			nextTarget := targets[nextTargetIndex]
			_, fallbackEmitErr := emit.emit(event.FallbackActivated, completed.ID, event.FallbackActivatedPayload{
				FailedCallID: callID, From: target.Identity, To: nextTarget.Identity,
				FallbackIndex: nextTargetIndex, Reason: decision.Reason, Error: eventError(runtimeError),
			})
			if fallbackEmitErr != nil {
				return eventSinkGeneration(fallbackEmitErr)
			}
			targetIndex = nextTargetIndex
			attempt = 0
		}
	}
}

func callModel(ctx context.Context, value model.Model, request model.Request, sink model.DeltaSink) (response model.Response, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("model adapter panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return value.Generate(ctx, request, sink)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func providerErrorCopy(err error) *model.ProviderError {
	var provider *model.ProviderError
	if !errors.As(err, &provider) || provider == nil {
		return nil
	}
	cloned := *provider
	return &cloned
}

func canceledModelGeneration(code string, cause error) modelGeneration {
	return modelGeneration{
		err:      &RunError{Class: ErrorCanceled, Code: code, SafeMessage: "The model call was canceled.", Cause: cause},
		reason:   "context_canceled",
		canceled: true,
	}
}

func eventSinkGeneration(cause error) modelGeneration {
	return modelGeneration{
		err:    &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: cause},
		reason: "event_sink_failed",
	}
}

func cloneToolDefinitions(values []model.ToolDefinition) []model.ToolDefinition {
	cloned := make([]model.ToolDefinition, len(values))
	for index, value := range values {
		cloned[index] = value.Clone()
	}
	return cloned
}
