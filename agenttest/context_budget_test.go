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

func TestValidateContextBudgetAcceptsManifestWithinLimits(t *testing.T) {
	t.Parallel()

	events := []event.Event{
		contextBudgetEvent(t, "evt_1", event.ModelCallStarted, event.ModelCallPayload{
			CallID: "model_1", Model: model.Identity{Provider: "test", Model: "scripted"}, Attempt: 1,
			Context: validBudgetContext(),
		}),
	}
	err := ValidateContextBudget(events, ContextBudget{
		InstructionStack: InstructionStackBudget{
			MaxBytes:             100,
			MaxSystemPromptBytes: 10,
			MaxProjectBytes:      20,
			MaxPolicyBytes:       30,
		},
		MaxToolDefinitionBytes:    40,
		MaxToolArgumentBytes:      20,
		MaxTextBytes:              100,
		MaxRequestMessages:        6,
		MaxProjectedMessages:      6,
		MaxToolDefinitions:        2,
		MaxRecalledMemoryMessages: 2,
		MaxCompressionSummaries:   2,
		MaxToolResultBlocks:       1,
		MaxArtifactBlocks:         1,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateContextBudgetRejectsExceededToolDefinitionBytes(t *testing.T) {
	t.Parallel()

	events := []event.Event{
		contextBudgetEvent(t, "evt_1", event.ModelCallStarted, event.ModelCallPayload{
			CallID: "model_1", Model: model.Identity{Provider: "test", Model: "scripted"}, Attempt: 1,
			Context: validBudgetContext(),
		}),
	}
	err := ValidateContextBudget(events, ContextBudget{MaxToolDefinitionBytes: 31})
	if err == nil || !strings.Contains(err.Error(), "ToolDefinitionBytes = 32 exceeds budget 31") {
		t.Fatalf("ValidateContextBudget() error = %v", err)
	}
}

func TestValidateContextBudgetRejectsExceededMemoryAndSummaryCounts(t *testing.T) {
	t.Parallel()

	events := []event.Event{
		contextBudgetEvent(t, "evt_1", event.ModelCallStarted, event.ModelCallPayload{
			CallID: "model_1", Model: model.Identity{Provider: "test", Model: "scripted"}, Attempt: 1,
			Context: validBudgetContext(),
		}),
	}
	err := ValidateContextBudget(events, ContextBudget{MaxRecalledMemoryMessages: 1, MaxCompressionSummaries: 0})
	if err == nil || !strings.Contains(err.Error(), "RecalledMemoryMessages = 2 exceeds budget 1") {
		t.Fatalf("ValidateContextBudget() error = %v", err)
	}
	err = ValidateContextBudget(events, ContextBudget{MaxRecalledMemoryMessages: 0, MaxCompressionSummaries: 1})
	if err == nil || !strings.Contains(err.Error(), "CompressionSummaries = 2 exceeds budget 1") {
		t.Fatalf("ValidateContextBudget() error = %v", err)
	}
	err = ValidateContextBudget(events, ContextBudget{MaxRecalledMemoryMessages: 0, MaxCompressionSummaries: 0, MaxRequestMessages: 5})
	if err == nil || !strings.Contains(err.Error(), "RequestMessageCount = 6 exceeds budget 5") {
		t.Fatalf("ValidateContextBudget() error = %v", err)
	}
	err = ValidateContextBudget(events, ContextBudget{MaxRecalledMemoryMessages: 0, MaxCompressionSummaries: 0, MaxToolDefinitions: 1})
	if err == nil || !strings.Contains(err.Error(), "ToolDefinitionCount = 2 exceeds budget 1") {
		t.Fatalf("ValidateContextBudget() error = %v", err)
	}
}

func TestValidateContextBudgetRejectsInvalidBudget(t *testing.T) {
	t.Parallel()

	err := ValidateContextBudget(nil, ContextBudget{MaxToolArgumentBytes: -1})
	if err == nil || !strings.Contains(err.Error(), "MaxToolArgumentBytes cannot be negative") {
		t.Fatalf("ValidateContextBudget() error = %v", err)
	}
	err = ValidateContextBudget(nil, ContextBudget{MaxRequestMessages: -1})
	if err == nil || !strings.Contains(err.Error(), "MaxRequestMessages cannot be negative") {
		t.Fatalf("ValidateContextBudget() error = %v", err)
	}
}

func TestValidateContextBudgetRequiresModelCall(t *testing.T) {
	t.Parallel()

	events := []event.Event{
		contextBudgetEvent(t, "evt_1", event.RunStarted, nil),
	}
	err := ValidateContextBudget(events, ContextBudget{MaxTextBytes: 100})
	if err == nil || !strings.Contains(err.Error(), "requires at least one model.call.started") {
		t.Fatalf("ValidateContextBudget() error = %v", err)
	}
}

func validBudgetContext() *event.ModelContextPayload {
	return &event.ModelContextPayload{
		ProjectedMessageCount: 6, RequestMessageCount: 6,
		SystemMessageCount:                5,
		UserMessageCount:                  1,
		PublicMessageCount:                1,
		InternalMessageCount:              5,
		InstructionStackMessages:          1,
		InstructionStackBytes:             64,
		InstructionStackSystemPromptBytes: 8,
		InstructionStackProjectBytes:      12,
		InstructionStackPolicyBytes:       16,
		RecalledMemoryMessages:            2,
		CompressionSummaries:              2,
		TextBlocks:                        6,
		TextBytes:                         64,
		ToolArgumentBytes:                 10,
		ToolResultBlocks:                  1,
		ArtifactBlocks:                    1,
		ToolDefinitionCount:               2,
		ToolDefinitionBytes:               32,
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
