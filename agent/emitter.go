package agent

import (
	"encoding/json"
	"fmt"
	"runtime/debug"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

// emitter 为单次 Run 生成有序生命周期事件，并同步投递给 Sink。
//
// 不变量：
//   - Sequence 从 1 起严格递增
//   - 终态事件之后禁止再 emit
//   - Sink 一旦失败，sinkErr 被锁存，后续不再调用 Sink（事件仍记入 events）
type emitter struct {
	runID    string        // 本 Run 的 ID，写入每条 Event.RunID
	ids      IDGenerator   // 生成 Event.ID
	now      Clock         // CreatedAt 用 UTC
	sink     event.Sink    // 可为 nil（Run 无流式消费者）
	sinkErr  error         // 首次 Sink 失败后锁存
	sequence int64         // 下一条事件序号（emit 时先 +1）
	terminal bool          // 是否已发出终态事件
	events   []event.Event // 本 Run 已发出事件的本地副本（供 Result.Events）
}

// emit 构造、校验并投递一条事件；payload 会被 JSON 序列化进 Event.Payload。
// 返回的 Event 是已校验快照；若 Sink 失败，仍返回该 Event，同时 error != nil。
func (e *emitter) emit(eventType event.Type, parentID string, payload any) (event.Event, error) {
	if e.terminal {
		return event.Event{}, fmt.Errorf("cannot emit %q after terminal event", eventType)
	}
	// 四种 Run 终态：之后任何 emit 都会被拒绝。
	if eventType == event.RunCompleted || eventType == event.RunFailed || eventType == event.RunCanceled || eventType == event.RunSuspended {
		e.terminal = true
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return event.Event{}, fmt.Errorf("marshal %s payload: %w", eventType, err)
	}
	e.sequence++
	value := event.Event{
		ProtocolVersion: event.ProtocolV1,
		ID:              e.ids("evt"),
		RunID:           e.runID,
		ParentID:        parentID, // 可选：关联父事件（如 ModelCallCompleted 挂在 Started 上）
		Type:            eventType,
		Sequence:        e.sequence,
		CreatedAt:       e.now().UTC(),
		Payload:         raw,
	}
	if err := value.Validate(); err != nil {
		return event.Event{}, err
	}
	e.events = append(e.events, cloneEvent(value)) // 本地账本始终保留
	if e.sink != nil && e.sinkErr == nil {
		// 传副本给 Sink，避免消费者改 Payload 影响本地 events。
		if err := callEventSink(e.sink, cloneEvent(value)); err != nil {
			e.sinkErr = err // 锁存：后续 emit 不再调用 Sink
			return value, err
		}
	}
	return value, nil
}

// callEventSink 同步调用 Sink，并把 panic 转成 error，避免冲垮 Agent 进程。
func callEventSink(sink event.Sink, value event.Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("event sink panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return sink(value)
}

// cloneEvent 深拷贝 Payload 字节，其余字段值拷贝。
func cloneEvent(value event.Event) event.Event {
	cloned := value
	cloned.Payload = append(json.RawMessage(nil), value.Payload...)
	return cloned
}

// cloneEvents 深拷贝事件切片，供 Result.Events 返回给调用方。
func cloneEvents(values []event.Event) []event.Event {
	cloned := make([]event.Event, len(values))
	for index, value := range values {
		cloned[index] = cloneEvent(value)
	}
	return cloned
}
