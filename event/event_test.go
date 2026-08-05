package event_test

import (
	"math"
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
		InstructionStackMessages: 1, InstructionStackBytes: 8,
		InstructionStackSystemPromptBytes: 4, InstructionStackPolicyBytes: 4,
		TextBlocks: 2, TextBytes: 10,
	}
	if err := payload.Validate(); err != nil {
		t.Fatal(err)
	}
	payload.RequestMessageCount = 3
	if err := payload.Validate(); err == nil {
		t.Fatal("ModelContextPayload accepted more Request Messages than projected Messages")
	}
	payload.RequestMessageCount = 2
	payload.InstructionStackMessages = 0
	if err := payload.Validate(); err == nil {
		t.Fatal("ModelContextPayload accepted Instruction Stack bytes without an Instruction Stack message")
	}
	payload.InstructionStackBytes = 0
	if err := payload.Validate(); err == nil {
		t.Fatal("ModelContextPayload accepted Instruction Stack kind bytes without an Instruction Stack message")
	}
	payload.InstructionStackMessages = 1
	payload.InstructionStackBytes = 8
	payload.InstructionStackSystemPromptBytes = 7
	payload.InstructionStackPolicyBytes = 7
	if err := payload.Validate(); err == nil {
		t.Fatal("ModelContextPayload accepted Instruction Stack kind bytes exceeding total bytes")
	}
	payload.InstructionStackBytes = math.MaxInt64
	payload.InstructionStackSystemPromptBytes = math.MaxInt64
	payload.InstructionStackPolicyBytes = 1
	if err := payload.Validate(); err == nil {
		t.Fatal("ModelContextPayload accepted overflowing Instruction Stack kind bytes")
	}
}
