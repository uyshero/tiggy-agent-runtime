package agent

import (
	"encoding/json"
	"fmt"
	"runtime/debug"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

type emitter struct {
	runID    string
	ids      IDGenerator
	now      Clock
	sink     event.Sink
	sinkErr  error
	sequence int64
	terminal bool
	events   []event.Event
}

func (e *emitter) emit(eventType event.Type, parentID string, payload any) (event.Event, error) {
	if e.terminal {
		return event.Event{}, fmt.Errorf("cannot emit %q after terminal event", eventType)
	}
	if eventType == event.RunCompleted || eventType == event.RunFailed || eventType == event.RunCanceled {
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
		ParentID:        parentID,
		Type:            eventType,
		Sequence:        e.sequence,
		CreatedAt:       e.now().UTC(),
		Payload:         raw,
	}
	if err := value.Validate(); err != nil {
		return event.Event{}, err
	}
	e.events = append(e.events, cloneEvent(value))
	if e.sink != nil && e.sinkErr == nil {
		if err := callEventSink(e.sink, cloneEvent(value)); err != nil {
			e.sinkErr = err
			return value, err
		}
	}
	return value, nil
}

func callEventSink(sink event.Sink, value event.Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("event sink panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return sink(value)
}

func cloneEvent(value event.Event) event.Event {
	cloned := value
	cloned.Payload = append(json.RawMessage(nil), value.Payload...)
	return cloned
}

func cloneEvents(values []event.Event) []event.Event {
	cloned := make([]event.Event, len(values))
	for index, value := range values {
		cloned[index] = cloneEvent(value)
	}
	return cloned
}
