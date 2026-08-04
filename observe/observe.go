// Package observe provides read-only consumers for Agent lifecycle events.
package observe

import (
	"encoding/json"
	"fmt"
	"runtime/debug"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

// ErrorHandler receives a failure swallowed by BestEffort. It must return
// promptly. A panic from the handler is contained because diagnostics must not
// escape the best-effort boundary.
type ErrorHandler func(error)

// Chain calls each non-nil Sink in order with an owned Event copy. The first
// error stops the chain and is returned to the Agent's strict Sink boundary.
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

// BestEffort converts Sink errors and panics into diagnostics and always
// returns nil. Use it for monitoring that must not change Agent execution.
// Strict audit or delivery consumers should be passed directly to the Agent.
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
