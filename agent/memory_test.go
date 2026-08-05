package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/contextassembly"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/memory"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

var testMemoryScope = memory.Scope{Namespace: "workspace", Key: "tenant-1:user-1"}

func TestMemoryRecallIsRequestOnlyAndObserveSeesCurrentTurn(t *testing.T) {
	t.Parallel()

	var observed memory.ObserveRequest
	memoryPort := agenttest.FuncMemory{
		RecallFunc: func(_ context.Context, request memory.RecallRequest) (memory.RecallResult, error) {
			if request.Scope != testMemoryScope || request.Input.Content[0].Text != "new question" {
				return memory.RecallResult{}, fmt.Errorf("unexpected recall request: %#v", request)
			}
			request.Input.Content[0].Text = "mutated by adapter"
			return memory.RecallResult{Items: []memory.Item{{
				ID: "memory-1", Scope: testMemoryScope, Text: "remembered evidence", Provenance: memory.Provenance{Source: "profile"},
			}}}, nil
		},
		ObserveFunc: func(_ context.Context, request memory.ObserveRequest) error {
			observed = request.Clone()
			request.Messages[0].Content[0].Text = "mutated by adapter"
			return nil
		},
	}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			if len(request.Messages) != 5 {
				return fmt.Errorf("message count = %d, want 5", len(request.Messages))
			}
			if request.Messages[0].Role != message.RoleSystem || request.Messages[1].Role != message.RoleSystem {
				return fmt.Errorf("memory was not inserted after leading System messages")
			}
			if !strings.Contains(request.Messages[1].Content[0].Text, "remembered evidence") {
				return fmt.Errorf("recalled memory missing from model request")
			}
			return nil
		},
		Response: textResponse("new answer"),
	})
	value := newMemoryAgent(t, script, memoryPort, memory.Options{}, "unused prompt")
	previous := agent.State{Messages: []message.Message{
		{ID: "system_1", Role: message.RoleSystem, Visibility: message.VisibilityInternal, Content: []message.Content{{Type: message.ContentText, Text: "existing system"}}},
		{ID: "old_user", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "old question"}}},
		{ID: "old_assistant", Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "old answer"}}},
	}}

	result, err := value.Continue(context.Background(), previous, userMessage("new question"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted || result.Final == nil {
		t.Fatalf("result = %#v", result)
	}
	for _, value := range result.State.Messages {
		for _, content := range value.Content {
			if strings.Contains(content.Text, "remembered evidence") || strings.Contains(content.Text, "mutated by adapter") {
				t.Fatalf("Memory projection or adapter mutation entered State: %#v", value)
			}
		}
	}
	if len(observed.Messages) != 2 || observed.Messages[0].Role != message.RoleUser || observed.Messages[0].Content[0].Text != "new question" ||
		observed.Messages[1].Role != message.RoleAssistant || observed.Messages[1].Content[0].Text != "new answer" {
		t.Fatalf("observed messages = %#v", observed.Messages)
	}
	recallIndex := eventTypeIndex(result.Events, event.MemoryRecallStarted)
	modelIndex := eventTypeIndex(result.Events, event.ModelCallStarted)
	modelCompletedIndex := eventTypeIndex(result.Events, event.ModelCallCompleted)
	observeIndex := eventTypeIndex(result.Events, event.MemoryObserveStarted)
	if recallIndex < 0 || modelIndex <= recallIndex || modelCompletedIndex <= modelIndex || observeIndex <= modelCompletedIndex {
		t.Fatalf("unexpected Memory lifecycle ordering: %v", agenttest.EventTypes(result.Events))
	}
	assertTerminalInvariants(t, result)
}

func TestInstructionStackKeepsMemoryBelowDynamicBoundary(t *testing.T) {
	t.Parallel()

	port := agenttest.FuncMemory{
		RecallFunc: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
			return memory.RecallResult{Items: []memory.Item{{
				ID: "memory-1", Scope: testMemoryScope, Text: "dynamic remembered evidence",
				Provenance: memory.Provenance{Source: "profile"},
			}}}, nil
		},
	}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			if len(request.Messages) != 3 {
				return fmt.Errorf("message count = %d, want 3", len(request.Messages))
			}
			stack := request.Messages[0]
			if stack.Role != message.RoleSystem || !strings.Contains(stack.Content[0].Text, contextassembly.DynamicBoundary) {
				return fmt.Errorf("first message is not an instruction stack: %#v", stack)
			}
			var metadata contextassembly.Metadata
			if err := json.Unmarshal(stack.Metadata, &metadata); err != nil {
				return err
			}
			if metadata.Kind != contextassembly.MetadataKindInstructionStack {
				return fmt.Errorf("instruction metadata = %#v", metadata)
			}
			recalled := request.Messages[1]
			if recalled.Role != message.RoleSystem || !strings.Contains(recalled.Content[0].Text, "dynamic remembered evidence") {
				return fmt.Errorf("second message is not recalled memory: %#v", recalled)
			}
			if strings.Index(stack.Content[0].Text, "Stable runtime rule.") > strings.Index(stack.Content[0].Text, contextassembly.DynamicBoundary) {
				return fmt.Errorf("static rule appears below dynamic boundary:\n%s", stack.Content[0].Text)
			}
			return nil
		},
		Response: textResponse("done"),
	})
	value := newMemoryAgent(t, script, port, memory.Options{}, "Stable runtime rule.")
	result, err := value.Run(context.Background(), userMessage("hello"))
	if err != nil || result.Status != agent.StatusCompleted {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	assertTerminalInvariants(t, result)
}

func TestMemoryRecallFailureModesAndInvalidResult(t *testing.T) {
	t.Parallel()

	t.Run("degrade by default", func(t *testing.T) {
		port := agenttest.FuncMemory{RecallFunc: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
			return memory.RecallResult{}, errors.New("backend unavailable")
		}}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")}), port, memory.Options{}, "")
		result, err := value.Run(context.Background(), userMessage("hello"))
		if err != nil || result.Status != agent.StatusCompleted {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
		payload := memoryEventPayload(t, result.Events, event.MemoryRecallCompleted)
		if payload.Status != "degraded" || payload.Error == nil {
			t.Fatalf("payload = %#v", payload)
		}
		assertTerminalInvariants(t, result)
	})

	t.Run("fail run", func(t *testing.T) {
		port := agenttest.FuncMemory{RecallFunc: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
			return memory.RecallResult{}, errors.New("backend unavailable")
		}}
		options := memory.Options{RecallFailureMode: memory.FailureModeFailRun}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(), port, options, "")
		result, err := value.Run(context.Background(), userMessage("hello"))
		assertRunError(t, err, agent.ErrorMemory, "memory_recall_failed")
		if result.Status != agent.StatusFailed {
			t.Fatalf("status = %q", result.Status)
		}
		assertTerminalInvariants(t, result)
	})

	t.Run("scope mismatch is always fatal", func(t *testing.T) {
		port := agenttest.FuncMemory{RecallFunc: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
			return memory.RecallResult{Items: []memory.Item{{
				ID: "memory-1", Scope: memory.Scope{Namespace: "workspace", Key: "another-user"},
				Text: "crossed scope", Provenance: memory.Provenance{Source: "test"},
			}}}, nil
		}}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(), port, memory.Options{}, "")
		result, err := value.Run(context.Background(), userMessage("hello"))
		assertRunError(t, err, agent.ErrorMemory, "invalid_memory_recall_result")
		if result.Status != agent.StatusFailed {
			t.Fatalf("status = %q", result.Status)
		}
		assertTerminalInvariants(t, result)
	})
}

func TestMemoryRecallFiltersExpiredItemsFromAnyPort(t *testing.T) {
	t.Parallel()

	expiredAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	port := agenttest.FuncMemory{RecallFunc: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
		return memory.RecallResult{Items: []memory.Item{{
			ID: "expired", Scope: testMemoryScope, Text: "must not reach the model",
			Provenance: memory.Provenance{Source: "test"}, ExpiresAt: &expiredAt,
		}}}, nil
	}}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			for _, value := range request.Messages {
				for _, content := range value.Content {
					if strings.Contains(content.Text, "must not reach the model") {
						return fmt.Errorf("expired memory entered model context")
					}
				}
			}
			return nil
		},
		Response: textResponse("done"),
	})
	value := newMemoryAgent(t, script, port, memory.Options{}, "")
	result, err := value.Run(context.Background(), userMessage("hello"))
	if err != nil || result.Status != agent.StatusCompleted {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	payload := memoryEventPayload(t, result.Events, event.MemoryRecallCompleted)
	if payload.Status != "empty" || payload.ItemCount != 0 || payload.ExpiredItemCount != 1 {
		t.Fatalf("payload = %#v", payload)
	}
	assertTerminalInvariants(t, result)
}

func TestMemoryCancellationAndPanicContainment(t *testing.T) {
	t.Parallel()

	t.Run("recall cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		port := agenttest.FuncMemory{RecallFunc: func(callCtx context.Context, _ memory.RecallRequest) (memory.RecallResult, error) {
			cancel()
			<-callCtx.Done()
			return memory.RecallResult{}, callCtx.Err()
		}}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(), port, memory.Options{}, "")
		result, err := value.Run(ctx, userMessage("hello"))
		assertRunError(t, err, agent.ErrorCanceled, "memory_recall_canceled")
		if result.Status != agent.StatusCanceled {
			t.Fatalf("status = %q", result.Status)
		}
		assertTerminalInvariants(t, result)
	})

	t.Run("recall panic", func(t *testing.T) {
		port := agenttest.FuncMemory{RecallFunc: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
			panic("recall boom")
		}}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(), port, memory.Options{}, "")
		result, err := value.Run(context.Background(), userMessage("hello"))
		assertRunError(t, err, agent.ErrorMemory, "memory_recall_panic")
		assertTerminalInvariants(t, result)
	})

	t.Run("observe panic", func(t *testing.T) {
		port := agenttest.FuncMemory{ObserveFunc: func(context.Context, memory.ObserveRequest) error {
			panic("observe boom")
		}}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")}), port, memory.Options{}, "")
		result, err := value.Run(context.Background(), userMessage("hello"))
		assertRunError(t, err, agent.ErrorMemory, "memory_observe_panic")
		if result.Status != agent.StatusFailed || len(result.State.Messages) != 2 {
			t.Fatalf("result = %#v", result)
		}
		assertTerminalInvariants(t, result)
	})

	t.Run("in-memory admission panic", func(t *testing.T) {
		port, err := memory.NewInMemory(memory.InMemoryOptions{Admission: memory.AdmissionFunc(
			func(context.Context, memory.ObserveRequest) ([]memory.Item, error) {
				panic("admission boom")
			},
		)})
		if err != nil {
			t.Fatal(err)
		}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")}), port, memory.Options{}, "")
		result, runErr := value.Run(context.Background(), userMessage("hello"))
		assertRunError(t, runErr, agent.ErrorMemory, "memory_observe_panic")
		if result.Status != agent.StatusFailed {
			t.Fatalf("status = %q", result.Status)
		}
		assertTerminalInvariants(t, result)
	})

	t.Run("in-memory recall clock panic", func(t *testing.T) {
		port, err := memory.NewInMemory(memory.InMemoryOptions{Now: func() time.Time {
			panic("clock boom")
		}})
		if err != nil {
			t.Fatal(err)
		}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(), port, memory.Options{}, "")
		result, runErr := value.Run(context.Background(), userMessage("hello"))
		assertRunError(t, runErr, agent.ErrorMemory, "memory_recall_panic")
		if result.Status != agent.StatusFailed {
			t.Fatalf("status = %q", result.Status)
		}
		assertTerminalInvariants(t, result)
	})

	t.Run("in-memory observe clock panic", func(t *testing.T) {
		clockCalls := 0
		port, err := memory.NewInMemory(memory.InMemoryOptions{
			Now: func() time.Time {
				clockCalls++
				if clockCalls == 2 {
					panic("clock boom")
				}
				return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			},
			Admission: memory.AdmissionFunc(func(context.Context, memory.ObserveRequest) ([]memory.Item, error) {
				return nil, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")}), port, memory.Options{}, "")
		result, runErr := value.Run(context.Background(), userMessage("hello"))
		assertRunError(t, runErr, agent.ErrorMemory, "memory_observe_panic")
		if result.Status != agent.StatusFailed || len(result.State.Messages) != 2 {
			t.Fatalf("result = %#v", result)
		}
		assertTerminalInvariants(t, result)
	})

	t.Run("in-memory invalid admission result", func(t *testing.T) {
		port, err := memory.NewInMemory(memory.InMemoryOptions{Admission: memory.AdmissionFunc(
			func(context.Context, memory.ObserveRequest) ([]memory.Item, error) {
				return []memory.Item{{
					ID: "crossed", Scope: memory.Scope{Namespace: "workspace", Key: "other"},
					Text: "invalid", Provenance: memory.Provenance{Source: "test"},
				}}, nil
			},
		)})
		if err != nil {
			t.Fatal(err)
		}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")}), port, memory.Options{}, "")
		result, runErr := value.Run(context.Background(), userMessage("hello"))
		assertRunError(t, runErr, agent.ErrorMemory, "invalid_memory_observe_result")
		if result.Status != agent.StatusFailed {
			t.Fatalf("status = %q", result.Status)
		}
		assertTerminalInvariants(t, result)
	})
}

func TestMemoryObserveFailureModesAndCancellation(t *testing.T) {
	t.Parallel()

	t.Run("degrade preserves completion", func(t *testing.T) {
		port := agenttest.FuncMemory{ObserveFunc: func(context.Context, memory.ObserveRequest) error {
			return errors.New("write unavailable")
		}}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")}), port, memory.Options{}, "")
		result, err := value.Run(context.Background(), userMessage("hello"))
		if err != nil || result.Status != agent.StatusCompleted || result.Final == nil {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
		payload := memoryEventPayload(t, result.Events, event.MemoryObserveFailed)
		if payload.Status != "degraded" {
			t.Fatalf("payload = %#v", payload)
		}
		assertTerminalInvariants(t, result)
	})

	t.Run("fail run after committing response", func(t *testing.T) {
		port := agenttest.FuncMemory{ObserveFunc: func(context.Context, memory.ObserveRequest) error {
			return errors.New("write unavailable")
		}}
		options := memory.Options{ObserveFailureMode: memory.FailureModeFailRun}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")}), port, options, "")
		result, err := value.Run(context.Background(), userMessage("hello"))
		assertRunError(t, err, agent.ErrorMemory, "memory_observe_failed")
		if result.Status != agent.StatusFailed || result.Final != nil || len(result.State.Messages) != 2 {
			t.Fatalf("result = %#v", result)
		}
		assertTerminalInvariants(t, result)
	})

	t.Run("cancellation always cancels", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		port := agenttest.FuncMemory{ObserveFunc: func(callCtx context.Context, _ memory.ObserveRequest) error {
			cancel()
			<-callCtx.Done()
			return callCtx.Err()
		}}
		value := newMemoryAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")}), port, memory.Options{}, "")
		result, err := value.Run(ctx, userMessage("hello"))
		assertRunError(t, err, agent.ErrorCanceled, "memory_observe_canceled")
		if result.Status != agent.StatusCanceled {
			t.Fatalf("status = %q", result.Status)
		}
		assertTerminalInvariants(t, result)
	})
}

func TestMemoryEventSinkFailureClosesInternalOperation(t *testing.T) {
	t.Parallel()

	value := newMemoryAgent(t, agenttest.NewScriptedModel(), agenttest.FuncMemory{}, memory.Options{}, "")
	result, err := value.RunStream(context.Background(), userMessage("hello"), func(value event.Event) error {
		if value.Type == event.MemoryRecallStarted {
			return errors.New("consumer failed")
		}
		return nil
	})
	assertRunError(t, err, agent.ErrorEventSink, "event_sink_failed")
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	assertTerminalInvariants(t, result)
}

func TestMemoryConfigRequiresPortAndValidScope(t *testing.T) {
	t.Parallel()

	base := agent.Config{
		Name: "test", Model: agenttest.NewScriptedModel(), ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
	}
	withOptions := base
	withOptions.MemoryOptions = memory.Options{RecallLimit: 1}
	if _, err := agent.New(withOptions); err == nil {
		t.Fatal("MemoryOptions without Memory was accepted")
	}
	withPort := base
	withPort.Memory = agenttest.FuncMemory{}
	if _, err := agent.New(withPort); err == nil {
		t.Fatal("Memory without valid MemoryScope was accepted")
	}
}

func TestInMemoryPortPersistsExplicitAdmissionAcrossRuns(t *testing.T) {
	t.Parallel()

	port, err := memory.NewInMemory(memory.InMemoryOptions{Admission: memory.AdmissionFunc(
		func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
			last := request.Messages[len(request.Messages)-1]
			return []memory.Item{{
				ID: "observation_" + request.RunID, Scope: request.Scope,
				Text: last.Content[0].Text, Provenance: memory.Provenance{
					Source: "test_admission", RunID: request.RunID, MessageIDs: []string{last.ID},
				},
			}}, nil
		},
	)})
	if err != nil {
		t.Fatal(err)
	}
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: textResponse("first answer")},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				for _, value := range request.Messages {
					if value.Role == message.RoleSystem && strings.Contains(value.Content[0].Text, "first answer") {
						return nil
					}
				}
				return fmt.Errorf("first observation was not recalled")
			},
			Response: textResponse("second answer"),
		},
	)
	value := newMemoryAgent(t, script, port, memory.Options{}, "")
	first, err := value.Run(context.Background(), userMessage("first question"))
	if err != nil || first.Status != agent.StatusCompleted {
		t.Fatalf("first Run = %#v, %v", first, err)
	}
	second, err := value.Run(context.Background(), userMessage("second question"))
	if err != nil || second.Status != agent.StatusCompleted {
		t.Fatalf("second Run = %#v, %v", second, err)
	}
	assertTerminalInvariants(t, first)
	assertTerminalInvariants(t, second)
}

func newMemoryAgent(
	t *testing.T,
	modelValue model.Model,
	memoryPort memory.Port,
	options memory.Options,
	systemPrompt string,
) *agent.Agent {
	t.Helper()
	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	value, err := agent.New(agent.Config{
		Name: "memory-test", SystemPrompt: systemPrompt, Model: modelValue,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"}, Toolkit: kit,
		Memory: memoryPort, MemoryScope: testMemoryScope, MemoryOptions: options,
		IDs: agenttest.SequentialIDs(), Now: agenttest.StepClock(time.Unix(1_700_000_000, 0), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func memoryEventPayload(t *testing.T, events []event.Event, eventType event.Type) event.MemoryPayload {
	t.Helper()
	for _, value := range events {
		if value.Type != eventType {
			continue
		}
		payload, err := agenttest.DecodePayload[event.MemoryPayload](value)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	t.Fatalf("event %q not found", eventType)
	return event.MemoryPayload{}
}

func eventTypeIndex(events []event.Event, eventType event.Type) int {
	for index, value := range events {
		if value.Type == eventType {
			return index
		}
	}
	return -1
}
