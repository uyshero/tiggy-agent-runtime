package event_test

import (
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

func TestTerminalEvents(t *testing.T) {
	t.Parallel()

	for _, eventType := range []event.Type{event.RunCompleted, event.RunFailed, event.RunCanceled, event.RunSuspended} {
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

func TestModelContextPayloadValidation(t *testing.T) {
	t.Parallel()

	payload := event.ModelContextPayload{
		ProjectedMessageCount: 2, RequestMessageCount: 2,
		SystemMessageCount: 1, UserMessageCount: 1,
		PublicMessageCount: 1, InternalMessageCount: 1,
		RecalledMemoryMessages: 1, TextBlocks: 2, TextBytes: 10,
	}
	if err := payload.Validate(); err != nil {
		t.Fatal(err)
	}
	payload.RequestMessageCount = 3
	if err := payload.Validate(); err == nil {
		t.Fatal("ModelContextPayload accepted more Request Messages than projected Messages")
	}
}
