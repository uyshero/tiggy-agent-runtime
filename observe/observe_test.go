package observe

import (
	"errors"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

func TestChainOwnsEventsAndStopsAtFirstFailure(t *testing.T) {
	t.Parallel()

	value := testEvent(t, "run_chain", "evt_chain", event.RunStarted, 1, time.Now(), "", event.RunStartedPayload{Agent: "agent"})
	called := 0
	sink := Chain(
		func(received event.Event) error {
			received.Payload[0] = 'x'
			called++
			return nil
		},
		func(received event.Event) error {
			called++
			if received.Payload[0] == 'x' {
				t.Fatal("observer mutation crossed the Chain boundary")
			}
			return errors.New("stop")
		},
		func(event.Event) error {
			called++
			return nil
		},
	)
	if err := sink(value); err == nil || called != 2 {
		t.Fatalf("Chain() error = %v, calls = %d", err, called)
	}
}

func TestBestEffortContainsSinkAndHandlerPanics(t *testing.T) {
	t.Parallel()

	handled := 0
	sink := BestEffort(func(event.Event) error {
		panic("sink boom")
	}, func(err error) {
		if err == nil {
			t.Fatal("missing observer error")
		}
		handled++
		panic("handler boom")
	})
	if err := sink(event.Event{}); err != nil || handled != 1 {
		t.Fatalf("BestEffort() error = %v, handled = %d", err, handled)
	}
}
