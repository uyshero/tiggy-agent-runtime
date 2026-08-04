// Package observe 提供 Agent 生命周期事件的只读消费者。
package observe

import (
	"encoding/json"
	"fmt"
	"runtime/debug"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

// ErrorHandler 接收 BestEffort 吞掉的失败；必须快速返回。
// handler panic 会被隔离，诊断不得冲出 best-effort 边界。
type ErrorHandler func(error)

// Chain 按顺序调用每个非 nil Sink，并传入事件副本；首个错误停止链路并交回 Agent 严格 Sink 边界。
func Chain(sinks ...event.Sink) event.Sink {
	active := make([]event.Sink, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			active = append(active, sink)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(value event.Event) error {
		for index, sink := range active {
			if err := callSink(sink, cloneEvent(value)); err != nil {
				return fmt.Errorf("observer %d: %w", index, err)
			}
		}
		return nil
	}
}

// BestEffort 将 Sink 错误与 panic 转为诊断并始终返回 nil。
// 用于不得改变 Agent 执行的监控；严格审计/投递消费者应直接传给 Agent。
func BestEffort(sink event.Sink, onError ErrorHandler) event.Sink {
	if sink == nil {
		return nil
	}
	return func(value event.Event) error {
		if err := callSink(sink, cloneEvent(value)); err != nil {
			callErrorHandler(onError, err)
		}
		return nil
	}
}

func callSink(sink event.Sink, value event.Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("observer panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return sink(value)
}

func callErrorHandler(handler ErrorHandler, err error) {
	if handler == nil || err == nil {
		return
	}
	defer func() { _ = recover() }()
	handler(err)
}

func cloneEvent(value event.Event) event.Event {
	cloned := value
	cloned.Payload = append(json.RawMessage(nil), value.Payload...)
	return cloned
}

func decode[T any](value event.Event) (T, error) {
	var payload T
	if err := json.Unmarshal(value.Payload, &payload); err != nil {
		return payload, fmt.Errorf("decode %s payload: %w", value.Type, err)
	}
	return payload, nil
}
