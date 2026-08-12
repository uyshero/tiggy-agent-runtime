package agent

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// modelGeneration 是一次（可能含重试/降级）模型生成的编排结果。
// 成功时填 response/callID/startedID；失败时填 err/reason（canceled 表示取消）。
type modelGeneration struct {
	response     model.Response
	callID       string // 本轮模型调用 ID
	startedID    string // ModelCallStarted 事件 ID，供 Completed 挂父
	target       model.Identity
	attempt      int    // 当前目标上的尝试序号（降级后会重置）
	transitionID string // model_call.completed Transition ID，供 State commit 关联
	err          *RunError
	reason       string
	canceled     bool
}

// modelDeltaBudget 限制单次 Generate 可进入事件轨迹的流式数据。
// err 会锁存首次超限；Provider 即使忽略 DeltaSink error，后续 Delta 也不会再 emit。
type modelDeltaBudget struct {
	mu       sync.Mutex
	count    int
	bytes    int64
	maxCount int
	maxBytes int64
	closed   bool
	err      *RunError
}

var errModelDeltaSinkClosed = errors.New("model DeltaSink called after Generate returned")

func newModelDeltaBudget(limits Limits) *modelDeltaBudget {
	return &modelDeltaBudget{
		maxCount: limits.MaxModelDeltasPerCall,
		maxBytes: limits.MaxModelDeltaBytesPerCall,
	}
}

func (b *modelDeltaBudget) accept(delta model.Delta) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errModelDeltaSinkClosed
	}
	if b.err != nil {
		return b.err
	}
	if b.count >= b.maxCount {
		b.err = &RunError{Class: ErrorLimit, Code: "max_model_deltas", SafeMessage: "The model stream exceeded its Delta count limit."}
		return b.err
	}
	deltaBytes, fits := modelDeltaPayloadBytes(delta, b.maxBytes-b.bytes)
	if !fits {
		b.err = &RunError{Class: ErrorLimit, Code: "max_model_delta_bytes", SafeMessage: "The model stream exceeded its Delta byte limit."}
		return b.err
	}
	b.count++
	b.bytes += deltaBytes
	return nil
}

// modelDeltaPayloadBytes 统计会进入事件的可变长 UTF-8 字符串，不复制正文。
// 固定结构开销由 Delta 数量上限约束。
func modelDeltaPayloadBytes(delta model.Delta, remaining int64) (int64, bool) {
	var fields [11]string
	fields[0] = string(delta.Type)
	fields[1] = delta.Text
	fields[2] = string(delta.StopReason)
	if delta.ToolCall != nil {
		fields[3] = delta.ToolCall.ID
		fields[4] = delta.ToolCall.Name
		fields[5] = delta.ToolCall.ArgumentsFragment
	}
	if delta.Usage != nil {
		fields[6] = string(delta.Usage.Source)
	}
	if delta.ProviderError != nil {
		fields[7] = string(delta.ProviderError.Class)
		fields[8] = delta.ProviderError.Code
		fields[9] = delta.ProviderError.RequestID
		fields[10] = delta.ProviderError.SafeDetail
	}
	var total int64
	for _, field := range fields {
		size := int64(len(field))
		if size > remaining-total {
			return 0, false
		}
		total += size
	}
	return total, true
}

func (b *modelDeltaBudget) closeAndFailure() *RunError {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return b.err
}

// generateModel 按主模型与 Fallbacks 调用供应商，并应用 FailurePolicy。
//
// 循环结构：
//  1. 检查取消与 MaxModelCalls
//  2. 若切换了目标，重新 prepareContext
//  3. 发 ModelCallStarted → Generate（流式 Delta）→ 成功返回或决策 Fail/Retry/Fallback
func (a *Agent) generateModel(
	ctx context.Context,
	runID string,
	state State,
	messages []message.Message,
	tools []model.ToolDefinition,
	transitions *transitionEmitter,
	emit *emitter,
	modelCalls *int,
) modelGeneration {
	// targets[0] 是主模型，其后是 Config.Fallbacks。
	targets := make([]ModelTarget, 0, len(a.config.Fallbacks)+1)
	targets = append(targets, ModelTarget{
		Model: a.config.Model, Identity: a.config.ModelIdentity,
		ContextPlanner: a.config.ContextPlanner, ContextCompressor: a.config.ContextCompressor,
	})
	targets = append(targets, a.config.Fallbacks...)
	targetIndex := 0
	attempt := 0
	plannedTargetIndex := -1 // 上次已做上下文规划的目标下标；变了才重规划
	contextRecoveryAttempted := make([]bool, len(targets))
	var preparedMessages []message.Message
	var preparedPlan contextmanager.Plan

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
		// 换目标后必须用该目标自己的 Planner/Compressor 重做上下文。
		if plannedTargetIndex != targetIndex {
			preparation := a.prepareContext(ctx, target, messages, tools, emit)
			if preparation.err != nil {
				return modelGeneration{err: preparation.err, reason: preparation.reason, canceled: preparation.canceled}
			}
			preparedMessages = preparation.messages
			preparedPlan = preparation.plan
			plannedTargetIndex = targetIndex
		}
		attempt++
		callID := a.config.IDs("model")
		idempotencyKey := stableTransitionKey("model-call", runID, callID)
		request := model.Request{
			RunID: runID, CallID: callID, Purpose: model.PurposeAgent, Model: target.Identity,
			Messages: message.CloneAll(preparedMessages), Tools: cloneToolDefinitions(tools),
			MaxOutputTokens: a.config.Limits.MaxOutputTokensPerCall,
		}
		preparedTransition, transitionErr := transitions.commit(ctx, TransitionModelCallPrepared, state, transitionFields{
			callID: callID, idempotencyKey: idempotencyKey,
			private: TransitionPrivateData{ModelRequest: &request},
		})
		if transitionErr != nil {
			return modelGeneration{err: transitionRunError("model_call_prepare_failed", transitionErr), reason: "transition_failed"}
		}
		started, err := emit.emit(event.ModelCallStarted, "", event.ModelCallPayload{
			CallID: callID, Model: target.Identity, Attempt: attempt,
			Context: modelContextSnapshot(len(messages), preparedMessages, tools),
		})
		if err != nil {
			_, _ = transitions.commit(context.WithoutCancel(ctx), TransitionModelCallCompleted, state, transitionFields{
				parentTransitionID: preparedTransition.ID,
				callID:             callID,
				idempotencyKey:     idempotencyKey,
				outcome:            "not_started",
			})
			_, _ = emit.emit(event.ModelCallCompleted, started.ID, event.ModelCallPayload{
				CallID: callID, Model: target.Identity, Attempt: attempt, Status: "failed",
				Error: &event.ErrorPayload{Class: string(ErrorEventSink), Code: "event_sink_failed", Message: "The event consumer failed."},
			})
			return eventSinkGeneration(err)
		}
		*modelCalls++

		// Delta 回调先应用本次调用预算，再同步发 ModelCallDelta。
		deltaBudget := newModelDeltaBudget(a.config.Limits)
		var deltaSinkMu sync.Mutex
		response, modelErr := callModel(ctx, target.Model, request, func(delta model.Delta) error {
			deltaSinkMu.Lock()
			defer deltaSinkMu.Unlock()
			if err := deltaBudget.accept(delta); err != nil {
				return err
			}
			_, err := emit.emit(event.ModelCallDelta, started.ID, event.ModelDeltaPayload{CallID: callID, Delta: delta})
			return err
		})
		// Generate 返回后关闭入口；并发/晚到的 Adapter 回调无法再改写事件序列。
		deltaSinkMu.Lock()
		streamErr := deltaBudget.closeAndFailure()
		deltaSinkMu.Unlock()
		if modelErr == nil && streamErr == nil {
			// Persist the exact normalized value that a recovery will commit. This
			// also makes repeated recovery attempts derive the same Message ID.
			response = a.normalizeResponse(response)
		}
		transitionOutcome := "completed"
		if ctx.Err() != nil {
			transitionOutcome = "canceled"
		} else if modelErr != nil {
			transitionOutcome = "failed"
		} else if streamErr != nil {
			transitionOutcome = "stream_failed"
		}
		privateData := TransitionPrivateData{}
		if modelErr == nil {
			privateData.ModelResponse = &response
		}
		completedTransition, transitionErr := transitions.commit(context.WithoutCancel(ctx), TransitionModelCallCompleted, state, transitionFields{
			parentTransitionID: preparedTransition.ID,
			callID:             callID,
			idempotencyKey:     idempotencyKey,
			outcome:            transitionOutcome,
			private:            privateData,
		})
		if transitionErr != nil {
			runtimeError := transitionRunError("model_call_complete_failed", transitionErr)
			_, _ = emit.emit(event.ModelCallCompleted, started.ID, event.ModelCallPayload{
				CallID: callID, Model: target.Identity, Attempt: attempt, Status: "failed", Error: eventError(runtimeError),
			})
			return modelGeneration{err: runtimeError, reason: "transition_failed"}
		}
		if emit.sinkErr != nil {
			_, _ = emit.emit(event.ModelCallCompleted, started.ID, event.ModelCallPayload{
				CallID: callID, Model: target.Identity, Attempt: attempt, Status: "failed",
				Error: &event.ErrorPayload{Class: string(ErrorEventSink), Code: "event_sink_failed", Message: "The event consumer failed."},
			})
			return eventSinkGeneration(emit.sinkErr)
		}
		if streamErr != nil {
			_, completedErr := emit.emit(event.ModelCallCompleted, started.ID, event.ModelCallPayload{
				CallID: callID, Model: target.Identity, Attempt: attempt, Status: "failed", Error: eventError(streamErr),
			})
			if completedErr != nil {
				return eventSinkGeneration(completedErr)
			}
			return modelGeneration{err: streamErr, reason: "model_delta_limit"}
		}
		if modelErr == nil {
			// 成功：不在这里发 Completed，由 runtime 在校验/记账后再发。
			return modelGeneration{
				response: response, callID: callID, startedID: started.ID,
				target: target.Identity, attempt: attempt, transitionID: completedTransition.ID,
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

		// 把失败交给 FailurePolicy，决定 fail / retry / fallback。
		failure := ModelFailure{
			TargetIndex: targetIndex, TargetCount: len(targets), Attempt: attempt,
			RemainingCalls: a.config.Limits.MaxModelCalls - *modelCalls,
			Error:          providerErrorCopy(modelErr), CanCompress: target.ContextPlanner != nil && target.ContextCompressor != nil,
			ContextRecoveryAttempted: contextRecoveryAttempted[targetIndex],
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
		case ModelFailureCompressRetry:
			if failure.RemainingCalls <= 0 {
				return modelGeneration{
					err:    &RunError{Class: ErrorLimit, Code: "max_model_calls", SafeMessage: "The Agent reached its model-call limit.", Cause: runtimeError},
					reason: "model_call_limit",
				}
			}
			recovery := a.compressContext(
				ctx, target, preparedMessages, tools, preparedPlan,
				contextmanager.CompressionTriggerProviderContextLength, completed.ID, emit,
			)
			if recovery.err != nil {
				return modelGeneration{err: recovery.err, reason: recovery.reason, canceled: recovery.canceled}
			}
			preparedMessages = recovery.messages
			preparedPlan = recovery.plan
			contextRecoveryAttempted[targetIndex] = true
			_, retryEmitErr := emit.emit(event.RetryScheduled, completed.ID, event.RetryScheduledPayload{
				FailedCallID: callID, Model: target.Identity, NextAttempt: attempt + 1,
				Reason: decision.Reason, Error: eventError(runtimeError),
			})
			if retryEmitErr != nil {
				return eventSinkGeneration(retryEmitErr)
			}
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
			// attempt 继续累加，同一 targetIndex 再试
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
			attempt = 0 // 新目标从第 1 次尝试重新计数
		}
	}
}

// callModel 隔离模型适配器 panic，避免冲垮 Agent 进程。
func callModel(ctx context.Context, value model.Model, request model.Request, sink model.DeltaSink) (response model.Response, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("model adapter panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return value.Generate(ctx, request, sink)
}

// waitForRetry 等待重试延迟，同时响应 Context 取消。
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

// providerErrorCopy 从 error 链取出 ProviderError 的值拷贝；没有则返回 nil。
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
