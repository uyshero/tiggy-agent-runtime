package agenttest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/memory"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestFaultInjectorValidatesRulesAndOrdinals(t *testing.T) {
	t.Parallel()

	injected := errors.New("planned failure")
	injector, err := NewFaultInjector(FaultRule{
		Point: FaultContextPlan, Call: 2, Action: FaultReturnError, Err: injected,
	})
	if err != nil {
		t.Fatal(err)
	}
	delegateCalls := 0
	planner, err := injector.WrapPlanner(FuncPlanner(func(_ context.Context, request contextmanager.Request) (contextmanager.Plan, error) {
		delegateCalls++
		return validFaultPlan(request), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := faultContextRequest()
	if _, err := planner.Plan(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), request); !errors.Is(err, injected) {
		t.Fatalf("second Plan error = %v", err)
	}
	if _, err := planner.Plan(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if delegateCalls != 2 || injector.Calls(FaultContextPlan) != 3 {
		t.Fatalf("delegate calls = %d, Injector calls = %d", delegateCalls, injector.Calls(FaultContextPlan))
	}

	for _, rules := range [][]FaultRule{
		{{Point: "unknown", Call: 1, Action: FaultPanic, PanicValue: "boom"}},
		{{Point: FaultContextPlan, Call: 0, Action: FaultWaitForCancel}},
		{{Point: FaultMemoryObserve, Call: 1, Action: FaultInvalidResult}},
		{
			{Point: FaultMemoryRecall, Call: 1, Action: FaultPanic, PanicValue: "one"},
			{Point: FaultMemoryRecall, Call: 1, Action: FaultPanic, PanicValue: "two"},
		},
	} {
		if _, err := NewFaultInjector(rules...); err == nil {
			t.Fatalf("NewFaultInjector accepted invalid rules: %#v", rules)
		}
	}
}

func TestFaultInjectorWatchMakesCancellationDeterministic(t *testing.T) {
	t.Parallel()

	injector, err := NewFaultInjector(FaultRule{
		Point: FaultTokenCount, Call: 1, Action: FaultWaitForCancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	reached, err := injector.Watch(FaultTokenCount, 1)
	if err != nil {
		t.Fatal(err)
	}
	counter, err := injector.WrapTokenCounter(FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) {
		return 10, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, callErr := counter.CountTokens(ctx, model.TokenRequest{})
		done <- callErr
	}()
	<-reached
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("CountTokens cancellation error = %v", err)
	}
	late, err := injector.Watch(FaultTokenCount, 1)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-late:
	default:
		t.Fatal("late Watch did not observe the completed invocation")
	}
}

func TestWaitForCancelRejectsNonCancelableContext(t *testing.T) {
	t.Parallel()

	injector, _ := NewFaultInjector(FaultRule{
		Point: FaultTokenCount, Call: 1, Action: FaultWaitForCancel,
	})
	counter, _ := injector.WrapTokenCounter(FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) {
		return 1, nil
	}))
	if _, err := counter.CountTokens(context.Background(), model.TokenRequest{}); err == nil {
		t.Fatal("wait_for_cancel accepted a non-cancelable Context")
	}
}

func TestFaultInjectorProducesInvalidContextAndMemoryResults(t *testing.T) {
	t.Parallel()

	injector, err := NewFaultInjector(
		FaultRule{Point: FaultContextPlan, Call: 1, Action: FaultInvalidResult},
		FaultRule{Point: FaultContextCompress, Call: 1, Action: FaultInvalidResult},
		FaultRule{Point: FaultTokenCount, Call: 1, Action: FaultInvalidResult},
		FaultRule{Point: FaultMemoryRecall, Call: 1, Action: FaultInvalidResult},
	)
	if err != nil {
		t.Fatal(err)
	}
	planner, _ := injector.WrapPlanner(FuncPlanner(func(_ context.Context, request contextmanager.Request) (contextmanager.Plan, error) {
		return validFaultPlan(request), nil
	}))
	if plan, err := planner.Plan(context.Background(), faultContextRequest()); err != nil || plan.Validate() == nil {
		t.Fatalf("invalid Plan = %#v, %v", plan, err)
	}
	compressor, _ := injector.WrapCompressor(FuncCompressor(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		return contextmanager.CompressionDecision{}, errors.New("must not be called")
	}))
	if decision, err := compressor.Compress(context.Background(), contextmanager.CompressionRequest{}); err != nil || decision != (contextmanager.CompressionDecision{}) {
		t.Fatalf("invalid Compression = %#v, %v", decision, err)
	}
	counter, _ := injector.WrapTokenCounter(FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) {
		return 10, nil
	}))
	if count, err := counter.CountTokens(context.Background(), model.TokenRequest{}); err != nil || count != 0 {
		t.Fatalf("invalid token count = %d, %v", count, err)
	}
	port, _ := injector.WrapMemory(memory.Noop{})
	request := faultRecallRequest()
	result, err := port.Recall(context.Background(), request)
	if err != nil || result.Validate(request, memory.DefaultOptions()) == nil {
		t.Fatalf("invalid Recall result = %#v, %v", result, err)
	}
}

func TestFaultErrorAfterDelegateExposesObserveSideEffectAmbiguity(t *testing.T) {
	t.Parallel()

	injected := errors.New("acknowledgement lost")
	injector, err := NewFaultInjector(FaultRule{
		Point: FaultMemoryObserve, Call: 1, Action: FaultErrorAfterDelegate, Err: injected,
	})
	if err != nil {
		t.Fatal(err)
	}
	observed := 0
	port, err := injector.WrapMemory(FuncMemory{ObserveFunc: func(context.Context, memory.ObserveRequest) error {
		observed++
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := port.Observe(context.Background(), faultObserveRequest()); !errors.Is(err, injected) {
		t.Fatalf("Observe() error = %v", err)
	}
	if observed != 1 || injector.Calls(FaultMemoryObserve) != 1 {
		t.Fatalf("Observe delegate calls = %d, Injector calls = %d", observed, injector.Calls(FaultMemoryObserve))
	}
}

func TestFaultInjectorIsRaceSafeAcrossCalls(t *testing.T) {
	t.Parallel()

	injector, _ := NewFaultInjector()
	var delegated atomic.Int64
	counter, _ := injector.WrapTokenCounter(FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) {
		delegated.Add(1)
		return 1, nil
	}))
	const calls = 128
	var group sync.WaitGroup
	for index := 0; index < calls; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := counter.CountTokens(context.Background(), model.TokenRequest{}); err != nil {
				t.Errorf("CountTokens(): %v", err)
			}
		}()
	}
	group.Wait()
	if delegated.Load() != calls || injector.Calls(FaultTokenCount) != calls {
		t.Fatalf("delegate calls = %d, Injector calls = %d", delegated.Load(), injector.Calls(FaultTokenCount))
	}
}

func faultContextRequest() contextmanager.Request {
	return contextmanager.Request{
		Model: model.Identity{Provider: "test", Model: "stable"},
		Messages: []message.Message{{
			ID: "user_1", Role: message.RoleUser, Visibility: message.VisibilityPublic,
			Content: []message.Content{{Type: message.ContentText, Text: "question"}},
		}},
		OutputReserveTokens: 10,
	}
}

func validFaultPlan(request contextmanager.Request) contextmanager.Plan {
	return contextmanager.Plan{
		Model: request.Model, CountSource: contextmanager.CountSourceEstimated,
		ContextWindowTokens: 100, InputTokens: 10, OutputReserveTokens: request.OutputReserveTokens,
		SafetyMarginTokens: 0, RequiredTokens: 20, AvailableInputTokens: 90,
		RemainingInputTokens: 80, OverflowTokens: 0, UtilizationBasisPoints: 1111,
		WarningThresholdBasisPoints: 8000, CriticalThresholdBasisPoints: 9500,
		Pressure: contextmanager.PressureNormal, Fits: true,
	}
}

func faultRecallRequest() memory.RecallRequest {
	return memory.RecallRequest{
		RunID: "run_fault", Scope: memory.Scope{Namespace: "workspace", Key: "user"}, Limit: 1,
		Input: message.Message{
			ID: "user_fault", Role: message.RoleUser, Visibility: message.VisibilityPublic,
			Content: []message.Content{{Type: message.ContentText, Text: "question"}},
		},
	}
}

func faultObserveRequest() memory.ObserveRequest {
	return memory.ObserveRequest{
		RunID: "run_fault", Scope: memory.Scope{Namespace: "workspace", Key: "user"},
		Messages: []message.Message{
			{ID: "user_fault", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "question"}}},
			{ID: "assistant_fault", Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "answer"}}},
		},
	}
}
