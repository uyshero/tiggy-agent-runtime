package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/memory"
	"github.com/uyshero/tiggy-agent-runtime/message"
)

// memoryRecall 是单次回忆编排结果：可选投影消息、终止错误与事件原因。
type memoryRecall struct {
	projection *message.Message
	err        *RunError
	reason     string
	canceled   bool
}

// memoryObservation 是单次观察编排结果。
type memoryObservation struct {
	err      *RunError
	reason   string
	canceled bool
}

// memoryPortPanic 捕获 Memory Port 适配器中的 panic，避免冲垮 Agent 进程。
type memoryPortPanic struct {
	operation string
	value     any
	stack     []byte
}

func (e *memoryPortPanic) Error() string {
	return fmt.Sprintf("memory %s adapter panic: %v\n%s", e.operation, e.value, e.stack)
}

// recallMemory 在 Run 开始时调用 Port.Recall，校验结果后投影为内部 System Message。
// FailureModeDegrade 下普通失败会跳过回忆并继续 Run。
func (a *Agent) recallMemory(ctx context.Context, runID string, input message.Message, emit *emitter) memoryRecall {
	if a.config.Memory == nil {
		return memoryRecall{}
	}
	options := a.config.MemoryOptions
	request := memory.RecallRequest{
		RunID: runID, Scope: a.config.MemoryScope, Input: message.Clone(input), Limit: options.RecallLimit,
	}
	payload := event.MemoryPayload{
		Namespace: request.Scope.Namespace, Limit: request.Limit, FailureMode: string(options.RecallFailureMode),
	}
	started, emitErr := emit.emit(event.MemoryRecallStarted, "", payload)
	if emitErr != nil {
		runtimeError := eventSinkRunError(emitErr)
		payload.Status = "failed"
		payload.Error = eventError(runtimeError)
		_, _ = emit.emit(event.MemoryRecallCompleted, started.ID, payload)
		return memoryRecall{err: runtimeError, reason: "event_sink_failed"}
	}
	if err := request.Validate(); err != nil {
		runtimeError := &RunError{
			Class: ErrorInternal, Code: "invalid_memory_recall_request",
			SafeMessage: "The Agent could not prepare a Memory recall request.",
			Cause:       &memory.Error{Code: memory.ErrorInvalidRequest, Cause: err},
		}
		return finishMemoryRecall(emit, started.ID, payload, "failed", runtimeError, memoryRecall{
			err: runtimeError, reason: "memory_recall_failed",
		})
	}

	result, recallErr := callMemoryRecall(ctx, a.config.Memory, request)
	if ctxErr := ctx.Err(); ctxErr != nil {
		runtimeError := &RunError{Class: ErrorCanceled, Code: "memory_recall_canceled", SafeMessage: "Memory recall was canceled.", Cause: ctxErr}
		return finishMemoryRecall(emit, started.ID, payload, "canceled", runtimeError, memoryRecall{
			err: runtimeError, reason: "context_canceled", canceled: true,
		})
	}
	if _, panicked := recallErr.(*memoryPortPanic); panicked {
		runtimeError := &RunError{Class: ErrorMemory, Code: "memory_recall_panic", SafeMessage: "The Memory adapter failed unexpectedly.", Cause: recallErr}
		return finishMemoryRecall(emit, started.ID, payload, "failed", runtimeError, memoryRecall{
			err: runtimeError, reason: "memory_recall_failed",
		})
	}
	var recallMemoryError *memory.Error
	if errors.As(recallErr, &recallMemoryError) && recallMemoryError.Code == memory.ErrorPanic {
		runtimeError := &RunError{Class: ErrorMemory, Code: "memory_recall_panic", SafeMessage: "The Memory adapter failed unexpectedly.", Cause: recallErr}
		return finishMemoryRecall(emit, started.ID, payload, "failed", runtimeError, memoryRecall{
			err: runtimeError, reason: "memory_recall_failed",
		})
	}
	if recallErr != nil {
		runtimeError := &RunError{Class: ErrorMemory, Code: "memory_recall_failed", SafeMessage: "Memory recall failed.", Cause: recallErr}
		if options.RecallFailureMode == memory.FailureModeDegrade {
			return finishMemoryRecall(emit, started.ID, payload, "degraded", runtimeError, memoryRecall{})
		}
		return finishMemoryRecall(emit, started.ID, payload, "failed", runtimeError, memoryRecall{
			err: runtimeError, reason: "memory_recall_failed",
		})
	}
	if err := result.Validate(request, options); err != nil {
		runtimeError := &RunError{
			Class: ErrorMemory, Code: "invalid_memory_recall_result",
			SafeMessage: "The Memory adapter returned an invalid recall result.",
			Cause:       &memory.Error{Code: memory.ErrorInvalidResult, Cause: err},
		}
		return finishMemoryRecall(emit, started.ID, payload, "failed", runtimeError, memoryRecall{
			err: runtimeError, reason: "memory_recall_failed",
		})
	}
	activeItems := make([]memory.Item, 0, len(result.Items))
	now := a.config.Now().UTC()
	for _, item := range result.Items {
		if item.Expired(now) {
			payload.ExpiredItemCount++
			continue
		}
		activeItems = append(activeItems, item.Clone())
	}
	payload.ItemCount, payload.TotalBytes = recalledSize(activeItems)
	if len(activeItems) == 0 {
		return finishMemoryRecall(emit, started.ID, payload, "empty", nil, memoryRecall{})
	}
	projected, err := memory.Project(a.config.IDs("msg"), request.Scope, activeItems)
	if err != nil {
		runtimeError := &RunError{
			Class: ErrorMemory, Code: "invalid_memory_recall_result",
			SafeMessage: "The Memory adapter returned an invalid recall result.", Cause: err,
		}
		return finishMemoryRecall(emit, started.ID, payload, "failed", runtimeError, memoryRecall{
			err: runtimeError, reason: "memory_recall_failed",
		})
	}
	return finishMemoryRecall(emit, started.ID, payload, "completed", nil, memoryRecall{projection: &projected})
}

// finishMemoryRecall 发出 MemoryRecallCompleted，并在 Sink 失败时覆盖为事件错误。
func finishMemoryRecall(
	emit *emitter,
	parentID string,
	payload event.MemoryPayload,
	status string,
	runtimeError *RunError,
	result memoryRecall,
) memoryRecall {
	payload.Status = status
	if runtimeError != nil {
		payload.Error = eventError(runtimeError)
	}
	if _, err := emit.emit(event.MemoryRecallCompleted, parentID, payload); err != nil {
		return memoryRecall{err: eventSinkRunError(err), reason: "event_sink_failed"}
	}
	return result
}

// observeMemory 在一轮完成后调用 Port.Observe；降级模式下普通失败不终止 Run。
func (a *Agent) observeMemory(
	ctx context.Context,
	runID string,
	messages []message.Message,
	emit *emitter,
) memoryObservation {
	if a.config.Memory == nil {
		return memoryObservation{}
	}
	options := a.config.MemoryOptions
	request := memory.ObserveRequest{RunID: runID, Scope: a.config.MemoryScope, Messages: message.CloneAll(messages)}
	encoded, encodeErr := json.Marshal(request.Messages)
	payload := event.MemoryPayload{
		Namespace: request.Scope.Namespace, MessageCount: len(request.Messages),
		ObservationBytes: int64(len(encoded)), FailureMode: string(options.ObserveFailureMode),
	}
	started, emitErr := emit.emit(event.MemoryObserveStarted, "", payload)
	if emitErr != nil {
		runtimeError := eventSinkRunError(emitErr)
		payload.Status = "failed"
		payload.Error = eventError(runtimeError)
		_, _ = emit.emit(event.MemoryObserveFailed, started.ID, payload)
		return memoryObservation{err: runtimeError, reason: "event_sink_failed"}
	}
	if encodeErr != nil {
		return a.finishMemoryObserveFailure(emit, started.ID, payload, "failed", &RunError{
			Class: ErrorInternal, Code: "invalid_memory_observe_request",
			SafeMessage: "The Agent could not prepare a Memory observation.", Cause: encodeErr,
		}, false)
	}
	if err := request.Validate(options); err != nil {
		runtimeError := &RunError{
			Class: ErrorMemory, Code: "memory_observation_rejected",
			SafeMessage: "The completed turn could not be observed by Memory.",
			Cause:       &memory.Error{Code: memory.ErrorLimit, Cause: err},
		}
		if options.ObserveFailureMode == memory.FailureModeDegrade {
			return a.finishMemoryObserveFailure(emit, started.ID, payload, "degraded", runtimeError, true)
		}
		return a.finishMemoryObserveFailure(emit, started.ID, payload, "failed", runtimeError, false)
	}

	observeErr := callMemoryObserve(ctx, a.config.Memory, request)
	if ctxErr := ctx.Err(); ctxErr != nil {
		runtimeError := &RunError{Class: ErrorCanceled, Code: "memory_observe_canceled", SafeMessage: "Memory observation was canceled.", Cause: ctxErr}
		return a.finishMemoryObserveFailure(emit, started.ID, payload, "canceled", runtimeError, false)
	}
	if _, panicked := observeErr.(*memoryPortPanic); panicked {
		runtimeError := &RunError{Class: ErrorMemory, Code: "memory_observe_panic", SafeMessage: "The Memory adapter failed unexpectedly.", Cause: observeErr}
		return a.finishMemoryObserveFailure(emit, started.ID, payload, "failed", runtimeError, false)
	}
	var observeMemoryError *memory.Error
	if errors.As(observeErr, &observeMemoryError) && observeMemoryError.Code == memory.ErrorPanic {
		runtimeError := &RunError{Class: ErrorMemory, Code: "memory_observe_panic", SafeMessage: "The Memory adapter failed unexpectedly.", Cause: observeErr}
		return a.finishMemoryObserveFailure(emit, started.ID, payload, "failed", runtimeError, false)
	}
	if errors.As(observeErr, &observeMemoryError) && observeMemoryError.Code == memory.ErrorInvalidResult {
		runtimeError := &RunError{Class: ErrorMemory, Code: "invalid_memory_observe_result", SafeMessage: "The Memory adapter produced an invalid observation result.", Cause: observeErr}
		return a.finishMemoryObserveFailure(emit, started.ID, payload, "failed", runtimeError, false)
	}
	if observeErr != nil {
		runtimeError := &RunError{Class: ErrorMemory, Code: "memory_observe_failed", SafeMessage: "Memory observation failed.", Cause: observeErr}
		if options.ObserveFailureMode == memory.FailureModeDegrade {
			return a.finishMemoryObserveFailure(emit, started.ID, payload, "degraded", runtimeError, true)
		}
		return a.finishMemoryObserveFailure(emit, started.ID, payload, "failed", runtimeError, false)
	}
	payload.Status = "completed"
	if _, err := emit.emit(event.MemoryObserveCompleted, started.ID, payload); err != nil {
		return memoryObservation{err: eventSinkRunError(err), reason: "event_sink_failed"}
	}
	return memoryObservation{}
}

// finishMemoryObserveFailure 发出 MemoryObserveFailed；degraded 为真时吞掉业务错误。
func (a *Agent) finishMemoryObserveFailure(
	emit *emitter,
	parentID string,
	payload event.MemoryPayload,
	status string,
	runtimeError *RunError,
	degraded bool,
) memoryObservation {
	payload.Status = status
	payload.Error = eventError(runtimeError)
	if _, err := emit.emit(event.MemoryObserveFailed, parentID, payload); err != nil {
		return memoryObservation{err: eventSinkRunError(err), reason: "event_sink_failed"}
	}
	if degraded {
		return memoryObservation{}
	}
	reason := "memory_observe_failed"
	if status == "canceled" {
		reason = "context_canceled"
	}
	return memoryObservation{
		err: runtimeError, reason: reason, canceled: status == "canceled",
	}
}

// callMemoryRecall 隔离 Port.Recall 的 panic，并返回结果深拷贝。
func callMemoryRecall(ctx context.Context, port memory.Port, request memory.RecallRequest) (result memory.RecallResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &memoryPortPanic{operation: "recall", value: recovered, stack: debug.Stack()}
		}
	}()
	result, err = port.Recall(ctx, request.Clone())
	return result.Clone(), err
}

// callMemoryObserve 隔离 Port.Observe 的 panic。
func callMemoryObserve(ctx context.Context, port memory.Port, request memory.ObserveRequest) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &memoryPortPanic{operation: "observe", value: recovered, stack: debug.Stack()}
		}
	}()
	return port.Observe(ctx, request.Clone())
}

// projectRecalledMemory 把回忆投影插到前导 System 消息之后，避免打乱既有系统提示顺序。
func projectRecalledMemory(messages []message.Message, projection *message.Message) []message.Message {
	values := message.CloneAll(messages)
	if projection == nil {
		return values
	}
	index := 0
	for index < len(values) && values[index].Role == message.RoleSystem {
		index++
	}
	values = append(values, message.Message{})
	copy(values[index+1:], values[index:])
	values[index] = message.Clone(*projection)
	return values
}

func recalledSize(items []memory.Item) (int, int64) {
	var total int64
	for _, item := range items {
		total += int64(len(item.Text))
	}
	return len(items), total
}
