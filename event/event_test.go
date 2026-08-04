package event_test

import (
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

func TestTerminalEvents(t *testing.T) {
	t.Parallel()

	for _, eventType := range []event.Type{event.RunCompleted, event.RunFailed, event.RunCanceled} {
		value := event.Event{
			ProtocolVersion: "tiggy.event.v1",
			ID:              "evt_1",
			RunID:           "run_1",
			Type:            eventType,
			Sequence:        1,
			CreatedAt:       time.Now(),
		}
		if err := value.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if !value.Terminal() {
			t.Fatalf("Terminal() = false for %q", eventType)
		}
	}
}
