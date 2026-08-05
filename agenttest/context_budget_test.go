package agenttest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestValidateInstructionStackBudgetAcceptsManifestWithinLimits(t *testing.T) {
	t.Parallel()

	events := []event.Event{
		contextBudgetEvent(t, "evt_1", event.RunStarted, nil),
		contextBudgetEvent(t, "evt_2", event.ModelCallStarted, event.ModelCallPayload{
			CallID: "model_1", Model: model.Identity{Provider: "test", Model: "scripted"}, Attempt: 1,
			Context: validBudgetContext(),
		}),
		contextBudgetEvent(t, "evt_3", event.ModelCallCompleted, event.ModelCallPayload{
			CallID: "model_1", Model: model.Identity{Provider: "test", Model: "scripted"}, Attempt: 1, Status: "completed",
		}),
		contextBudgetEvent(t, "evt_4", event.RunCompleted, nil),
	}
	err := ValidateInstructionStackBudget(events, InstructionStackBudget{
		MaxBytes:             100,
		MaxSystemPromptBytes: 10,
		MaxProjectBytes:      20,
		MaxPolicyBytes:       30,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateInstructionStackBudgetRejectsExceededLimit(t *testing.T) {
	t.Parallel()

	events := []event.Event{
		contextBudgetEvent(t, "evt_1", event.ModelCallStarted, event.ModelCallPayload{
			CallID: "model_1", Model: model.Identity{Provider: "test", Model: "scripted"}, Attempt: 1,
			Context: validBudgetContext(),
		}),
	}
	err := ValidateInstructionStackBudget(events, InstructionStackBudget{MaxProjectBytes: 5})
	if err == nil || !strings.Contains(err.Error(), "project bytes = 12 exceeds budget 5") {
		t.Fatalf("ValidateInstructionStackBudget() error = %v", err)
	}
}

func TestValidateInstructionStackBudgetRequiresManifest(t *testing.T) {
	t.Parallel()

	events := []event.Event{
		contextBudgetEvent(t, "evt_1", event.ModelCallStarted, event.ModelCallPayload{
			CallID: "model_1", Model: model.Identity{Provider: "test", Model: "scripted"}, Attempt: 1,
		}),
	}
	err := ValidateInstructionStackBudget(events, InstructionStackBudget{MaxBytes: 100})
	if err == nil || !strings.Contains(err.Error(), "has no Context Manifest") {
		t.Fatalf("ValidateInstructionStackBudget() error = %v", err)
	}
}

func TestValidateInstructionStackBudgetRejectsInvalidBudget(t *testing.T) {
	t.Parallel()

	err := ValidateInstructionStackBudget(nil, InstructionStackBudget{MaxBytes: -1})
	if err == nil || !strings.Contains(err.Error(), "cannot be negative") {
		t.Fatalf("ValidateInstructionStackBudget() error = %v", err)
	}
}

func validBudgetContext() *event.ModelContextPayload {
	return &event.ModelContextPayload{
		ProjectedMessageCount: 1, RequestMessageCount: 1,
		SystemMessageCount:                1,
		InternalMessageCount:              1,
		InstructionStackMessages:          1,
		InstructionStackBytes:             64,
		InstructionStackSystemPromptBytes: 8,
		InstructionStackProjectBytes:      12,
		InstructionStackPolicyBytes:       16,
		TextBlocks:                        1,
		TextBytes:                         64,
	}
}

func contextBudgetEvent(t *testing.T, id string, eventType event.Type, payload any) event.Event {
	t.Helper()

	var encoded json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		encoded = data
	}
	return event.Event{
		ProtocolVersion: event.ProtocolV1,
		ID:              id,
		RunID:           "run_1",
		Type:            eventType,
		Sequence:        1,
		CreatedAt:       time.Unix(1_700_000_000, 0),
		Payload:         encoded,
	}
}
