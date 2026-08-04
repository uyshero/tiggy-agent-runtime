package agenttest

import (
	"context"
	"fmt"
	"sync"

	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/memory"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// FaultPoint identifies one Context or Memory extension boundary.
type FaultPoint string

const (
	FaultContextPlan     FaultPoint = "context.plan"
	FaultContextCompress FaultPoint = "context.compress"
	FaultTokenCount      FaultPoint = "context.token_count"
	FaultMemoryRecall    FaultPoint = "memory.recall"
	FaultMemoryObserve   FaultPoint = "memory.observe"
)

type FaultAction string

const (
	// FaultReturnError fails before the delegate is called.
	FaultReturnError FaultAction = "return_error"
	// FaultPanic panics before the delegate is called.
	FaultPanic FaultAction = "panic"
	// FaultWaitForCancel blocks until Context cancellation and returns ctx.Err.
	FaultWaitForCancel FaultAction = "wait_for_cancel"
	// FaultInvalidResult returns an operation-specific invalid value with nil error.
	FaultInvalidResult FaultAction = "invalid_result"
	// FaultErrorAfterDelegate calls the delegate once and returns the injected
	// error only when the delegate succeeded.
	FaultErrorAfterDelegate FaultAction = "error_after_delegate"
)

// FaultRule triggers once at the exact one-based call ordinal for its Point.
type FaultRule struct {
	Point      FaultPoint
	Call       int
	Action     FaultAction
	Err        error
	PanicValue any
}

type faultKey struct {
	point FaultPoint
	call  int
}

// FaultError identifies a deliberate test failure without changing the
// wrapped error's identity.
type FaultError struct {
	Point FaultPoint
	Call  int
	Cause error
}

func (e *FaultError) Error() string {
	if e == nil {
		return "fault injection failed"
	}
	return fmt.Sprintf("fault injected at %s call %d: %v", e.Point, e.Call, e.Cause)
}

func (e *FaultError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// FaultInjector owns an immutable rule set and per-Point call ordinals. It is
// concurrency-safe; concurrent calls at one Point are ordered by lock entry,
// so deterministic scenarios should use one Injector per controlled flow.
type FaultInjector struct {
	mu       sync.Mutex
	rules    map[faultKey]FaultRule
	calls    map[FaultPoint]int
	watchers map[faultKey]chan struct{}
}

func NewFaultInjector(rules ...FaultRule) (*FaultInjector, error) {
	injector := &FaultInjector{
		rules: make(map[faultKey]FaultRule), calls: make(map[FaultPoint]int),
		watchers: make(map[faultKey]chan struct{}),
	}
	for index, rule := range rules {
		if err := validateFaultRule(rule); err != nil {
			return nil, fmt.Errorf("fault rule %d: %w", index, err)
		}
		key := faultKey{point: rule.Point, call: rule.Call}
		if _, exists := injector.rules[key]; exists {
			return nil, fmt.Errorf("fault rule %d duplicates %s call %d", index, rule.Point, rule.Call)
		}
		injector.rules[key] = rule
	}
	return injector, nil
}

// Watch returns a channel closed when the specified invocation enters the
// Injector. Register it before starting a FaultWaitForCancel call.
func (i *FaultInjector) Watch(point FaultPoint, call int) (<-chan struct{}, error) {
	if i == nil || !validFaultPoint(point) || call < 1 {
		return nil, fmt.Errorf("valid FaultInjector, Point, and positive call are required")
	}
	key := faultKey{point: point, call: call}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.calls[point] >= call {
		closed := make(chan struct{})
		close(closed)
		return closed, nil
	}
	if watcher := i.watchers[key]; watcher != nil {
		return watcher, nil
	}
	watcher := make(chan struct{})
	i.watchers[key] = watcher
	return watcher, nil
}

func (i *FaultInjector) Calls(point FaultPoint) int {
	if i == nil {
		return 0
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls[point]
}

func (i *FaultInjector) WrapPlanner(delegate contextmanager.Planner) (contextmanager.Planner, error) {
	if i == nil || delegate == nil {
		return nil, fmt.Errorf("FaultInjector and Context Planner are required")
	}
	return faultPlanner{injector: i, delegate: delegate}, nil
}

func (i *FaultInjector) WrapCompressor(delegate contextmanager.Compressor) (contextmanager.Compressor, error) {
	if i == nil || delegate == nil {
		return nil, fmt.Errorf("FaultInjector and Context Compressor are required")
	}
	return faultCompressor{injector: i, delegate: delegate}, nil
}

func (i *FaultInjector) WrapTokenCounter(delegate model.TokenCounter) (model.TokenCounter, error) {
	if i == nil || delegate == nil {
		return nil, fmt.Errorf("FaultInjector and Token Counter are required")
	}
	return faultTokenCounter{injector: i, delegate: delegate}, nil
}

func (i *FaultInjector) WrapMemory(delegate memory.Port) (memory.Port, error) {
	if i == nil || delegate == nil {
		return nil, fmt.Errorf("FaultInjector and Memory Port are required")
	}
	return faultMemory{injector: i, delegate: delegate}, nil
}

type faultDecision struct {
	rule FaultRule
	call int
	hit  bool
}

func (i *FaultInjector) decide(ctx context.Context, point FaultPoint) (faultDecision, error) {
	i.mu.Lock()
	i.calls[point]++
	call := i.calls[point]
	key := faultKey{point: point, call: call}
	rule, hit := i.rules[key]
	if watcher := i.watchers[key]; watcher != nil {
		close(watcher)
		delete(i.watchers, key)
	}
	i.mu.Unlock()

	decision := faultDecision{rule: rule, call: call, hit: hit}
	if !hit {
		return decision, nil
	}
	switch rule.Action {
	case FaultReturnError:
		return decision, injectedError(rule, call)
	case FaultPanic:
		panic(rule.PanicValue)
	case FaultWaitForCancel:
		if ctx == nil {
			return decision, &FaultError{Point: point, Call: call, Cause: fmt.Errorf("Context is required for cancellation fault")}
		}
		done := ctx.Done()
		if done == nil {
			return decision, &FaultError{Point: point, Call: call, Cause: fmt.Errorf("cancellable Context is required for cancellation fault")}
		}
		<-done
		return decision, ctx.Err()
	case FaultInvalidResult, FaultErrorAfterDelegate:
		return decision, nil
	default:
		return decision, &FaultError{Point: point, Call: call, Cause: fmt.Errorf("unsupported action %q", rule.Action)}
	}
}

func injectedError(rule FaultRule, call int) error {
	return &FaultError{Point: rule.Point, Call: call, Cause: rule.Err}
}

func validateFaultRule(rule FaultRule) error {
	if !validFaultPoint(rule.Point) || rule.Call < 1 {
		return fmt.Errorf("valid Point and positive call are required")
	}
	switch rule.Action {
	case FaultReturnError, FaultErrorAfterDelegate:
		if rule.Err == nil {
			return fmt.Errorf("action %q requires Err", rule.Action)
		}
	case FaultPanic:
		if rule.PanicValue == nil {
			return fmt.Errorf("panic action requires PanicValue")
		}
	case FaultWaitForCancel:
	case FaultInvalidResult:
		if rule.Point == FaultMemoryObserve {
			return fmt.Errorf("Memory Observe has no result to invalidate")
		}
	default:
		return fmt.Errorf("unsupported action %q", rule.Action)
	}
	return nil
}

func validFaultPoint(point FaultPoint) bool {
	switch point {
	case FaultContextPlan, FaultContextCompress, FaultTokenCount, FaultMemoryRecall, FaultMemoryObserve:
		return true
	default:
		return false
	}
}

type faultPlanner struct {
	injector *FaultInjector
	delegate contextmanager.Planner
}

func (f faultPlanner) Plan(ctx context.Context, request contextmanager.Request) (contextmanager.Plan, error) {
	decision, err := f.injector.decide(ctx, FaultContextPlan)
	if err != nil {
		return contextmanager.Plan{}, err
	}
	if decision.hit && decision.rule.Action == FaultInvalidResult {
		return contextmanager.Plan{}, nil
	}
	plan, delegateErr := f.delegate.Plan(ctx, cloneContextRequest(request))
	if delegateErr == nil && decision.hit && decision.rule.Action == FaultErrorAfterDelegate {
		return contextmanager.Plan{}, injectedError(decision.rule, decision.call)
	}
	return plan, delegateErr
}

type faultCompressor struct {
	injector *FaultInjector
	delegate contextmanager.Compressor
}

func (f faultCompressor) Compress(ctx context.Context, request contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
	decision, err := f.injector.decide(ctx, FaultContextCompress)
	if err != nil {
		return contextmanager.CompressionDecision{}, err
	}
	if decision.hit && decision.rule.Action == FaultInvalidResult {
		return contextmanager.CompressionDecision{}, nil
	}
	result, delegateErr := f.delegate.Compress(ctx, request.Clone())
	if delegateErr == nil && decision.hit && decision.rule.Action == FaultErrorAfterDelegate {
		return contextmanager.CompressionDecision{}, injectedError(decision.rule, decision.call)
	}
	return result, delegateErr
}

type faultTokenCounter struct {
	injector *FaultInjector
	delegate model.TokenCounter
}

func (f faultTokenCounter) CountTokens(ctx context.Context, request model.TokenRequest) (int64, error) {
	decision, err := f.injector.decide(ctx, FaultTokenCount)
	if err != nil {
		return 0, err
	}
	if decision.hit && decision.rule.Action == FaultInvalidResult {
		return 0, nil
	}
	count, delegateErr := f.delegate.CountTokens(ctx, cloneTokenRequest(request))
	if delegateErr == nil && decision.hit && decision.rule.Action == FaultErrorAfterDelegate {
		return 0, injectedError(decision.rule, decision.call)
	}
	return count, delegateErr
}

type faultMemory struct {
	injector *FaultInjector
	delegate memory.Port
}

func (f faultMemory) Recall(ctx context.Context, request memory.RecallRequest) (memory.RecallResult, error) {
	request = request.Clone()
	decision, err := f.injector.decide(ctx, FaultMemoryRecall)
	if err != nil {
		return memory.RecallResult{}, err
	}
	if decision.hit && decision.rule.Action == FaultInvalidResult {
		crossed := request.Scope
		crossed.Key = "fault-injected-cross-scope"
		if crossed == request.Scope {
			crossed.Namespace = "fault-injected-cross-scope"
		}
		return memory.RecallResult{Items: []memory.Item{{
			ID: "fault-invalid-recall", Scope: crossed, Text: "fault-injected invalid recall",
			Provenance: memory.Provenance{Source: "agenttest.fault"},
		}}}, nil
	}
	result, delegateErr := f.delegate.Recall(ctx, request.Clone())
	if delegateErr == nil && decision.hit && decision.rule.Action == FaultErrorAfterDelegate {
		return memory.RecallResult{}, injectedError(decision.rule, decision.call)
	}
	return result.Clone(), delegateErr
}

func (f faultMemory) Observe(ctx context.Context, request memory.ObserveRequest) error {
	request = request.Clone()
	decision, err := f.injector.decide(ctx, FaultMemoryObserve)
	if err != nil {
		return err
	}
	delegateErr := f.delegate.Observe(ctx, request.Clone())
	if delegateErr == nil && decision.hit && decision.rule.Action == FaultErrorAfterDelegate {
		return injectedError(decision.rule, decision.call)
	}
	return delegateErr
}

func cloneContextRequest(request contextmanager.Request) contextmanager.Request {
	cloned := request
	cloned.Messages = message.CloneAll(request.Messages)
	cloned.Tools = make([]model.ToolDefinition, len(request.Tools))
	for index, definition := range request.Tools {
		cloned.Tools[index] = definition.Clone()
	}
	return cloned
}
