package agenttest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/memory"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestContextFaultsReachStableAgentFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		action agenttest.FaultAction
		code   string
	}{
		{name: "invalid result", action: agenttest.FaultInvalidResult, code: "invalid_context_plan"},
		{name: "panic", action: agenttest.FaultPanic, code: "context_planner_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rule := agenttest.FaultRule{Point: agenttest.FaultContextPlan, Call: 1, Action: test.action, PanicValue: "planned panic"}
			injector, err := agenttest.NewFaultInjector(rule)
			if err != nil {
				t.Fatal(err)
			}
			planner, err := injector.WrapPlanner(agenttest.FuncPlanner(func(context.Context, contextmanager.Request) (contextmanager.Plan, error) {
				return contextmanager.Plan{}, errors.New("delegate must not be called")
			}))
			if err != nil {
				t.Fatal(err)
			}
			runtime := newFaultAgent(t, agenttest.NewScriptedModel(), planner, nil)
			result, runErr := runtime.Run(context.Background(), faultUserMessage())
			assertAgentError(t, runErr, agent.ErrorInternal, test.code)
			if result.Status != agent.StatusFailed || injector.Calls(agenttest.FaultContextPlan) != 1 {
				t.Fatalf("result = %#v, calls = %d", result, injector.Calls(agenttest.FaultContextPlan))
			}
			if err := agenttest.ValidateTrace(result.Events); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContextCancellationFaultUsesWatchWithoutTimingRace(t *testing.T) {
	t.Parallel()

	injector, err := agenttest.NewFaultInjector(agenttest.FaultRule{
		Point: agenttest.FaultContextPlan, Call: 1, Action: agenttest.FaultWaitForCancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	reached, err := injector.Watch(agenttest.FaultContextPlan, 1)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := injector.WrapPlanner(agenttest.FuncPlanner(func(context.Context, contextmanager.Request) (contextmanager.Plan, error) {
		return contextmanager.Plan{}, errors.New("delegate must not be called")
	}))
	if err != nil {
		t.Fatal(err)
	}
	runtime := newFaultAgent(t, agenttest.NewScriptedModel(), planner, nil)
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result agent.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, runErr := runtime.Run(ctx, faultUserMessage())
		done <- outcome{result: result, err: runErr}
	}()
	<-reached
	cancel()
	completed := <-done
	assertAgentError(t, completed.err, agent.ErrorCanceled, "context_prepare_canceled")
	if completed.result.Status != agent.StatusCanceled {
		t.Fatalf("status = %q", completed.result.Status)
	}
	if err := agenttest.ValidateTrace(completed.result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryFaultsPreserveDegradeAndFatalPolicies(t *testing.T) {
	t.Parallel()

	t.Run("ordinary recall error degrades", func(t *testing.T) {
		injected := errors.New("memory unavailable")
		injector, _ := agenttest.NewFaultInjector(agenttest.FaultRule{
			Point: agenttest.FaultMemoryRecall, Call: 1, Action: agenttest.FaultReturnError, Err: injected,
		})
		port, _ := injector.WrapMemory(memory.Noop{})
		runtime := newFaultAgent(t, faultModel(), nil, port)
		result, runErr := runtime.Run(context.Background(), faultUserMessage())
		if runErr != nil || result.Status != agent.StatusCompleted {
			t.Fatalf("result = %#v, error = %v", result, runErr)
		}
		payload := findMemoryPayload(t, result.Events, event.MemoryRecallCompleted)
		if payload.Status != "degraded" || payload.Error == nil {
			t.Fatalf("Memory payload = %#v", payload)
		}
		if err := agenttest.ValidateTrace(result.Events); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("invalid recall is fatal", func(t *testing.T) {
		injector, _ := agenttest.NewFaultInjector(agenttest.FaultRule{
			Point: agenttest.FaultMemoryRecall, Call: 1, Action: agenttest.FaultInvalidResult,
		})
		port, _ := injector.WrapMemory(memory.Noop{})
		runtime := newFaultAgent(t, agenttest.NewScriptedModel(), nil, port)
		result, runErr := runtime.Run(context.Background(), faultUserMessage())
		assertAgentError(t, runErr, agent.ErrorMemory, "invalid_memory_recall_result")
		if result.Status != agent.StatusFailed {
			t.Fatalf("status = %q", result.Status)
		}
		if err := agenttest.ValidateTrace(result.Events); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMemoryObserveAfterDelegateFaultIsNotReplayed(t *testing.T) {
	t.Parallel()

	injector, _ := agenttest.NewFaultInjector(agenttest.FaultRule{
		Point: agenttest.FaultMemoryObserve, Call: 1, Action: agenttest.FaultErrorAfterDelegate,
		Err: errors.New("observation acknowledgement lost"),
	})
	observed := 0
	port, _ := injector.WrapMemory(agenttest.FuncMemory{ObserveFunc: func(context.Context, memory.ObserveRequest) error {
		observed++
		return nil
	}})
	runtime := newFaultAgent(t, faultModel(), nil, port)
	result, runErr := runtime.Run(context.Background(), faultUserMessage())
	if runErr != nil || result.Status != agent.StatusCompleted {
		t.Fatalf("result = %#v, error = %v", result, runErr)
	}
	if observed != 1 || injector.Calls(agenttest.FaultMemoryObserve) != 1 {
		t.Fatalf("Observe side effects = %d, calls = %d", observed, injector.Calls(agenttest.FaultMemoryObserve))
	}
	payload := findMemoryPayload(t, result.Events, event.MemoryObserveFailed)
	if payload.Status != "degraded" || payload.Error == nil {
		t.Fatalf("Memory payload = %#v", payload)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func newFaultAgent(t *testing.T, provider model.Model, planner contextmanager.Planner, port memory.Port) *agent.Agent {
	t.Helper()
	config := agent.Config{
		Name: "fault-test", Model: provider,
		ModelIdentity:  model.Identity{Provider: "test", Model: "stable"},
		ContextPlanner: planner, IDs: agenttest.SequentialIDs(),
	}
	if port != nil {
		config.Memory = port
		config.MemoryScope = memory.Scope{Namespace: "workspace", Key: "user"}
	}
	value, err := agent.New(config)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func faultModel() model.Model {
	return agenttest.NewScriptedModel(agenttest.ModelStep{Response: model.Response{
		Message:    message.Message{Content: []message.Content{{Type: message.ContentText, Text: "done"}}},
		StopReason: model.StopReasonComplete,
	}})
}

func faultUserMessage() message.Message {
	return message.Message{
		Role: message.RoleUser, Visibility: message.VisibilityPublic,
		Content: []message.Content{{Type: message.ContentText, Text: "question"}},
	}
}

func assertAgentError(t *testing.T, err error, class agent.ErrorClass, code string) {
	t.Helper()
	var runError *agent.RunError
	if !errors.As(err, &runError) || runError.Class != class || runError.Code != code {
		t.Fatalf("error = %v, want %s/%s", err, class, code)
	}
}

func findMemoryPayload(t *testing.T, events []event.Event, eventType event.Type) event.MemoryPayload {
	t.Helper()
	for _, value := range events {
		if value.Type == eventType {
			payload, err := agenttest.DecodePayload[event.MemoryPayload](value)
			if err != nil {
				t.Fatal(err)
			}
			return payload
		}
	}
	t.Fatalf("Event %q not found", eventType)
	return event.MemoryPayload{}
}
