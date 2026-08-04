package contextmanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestTokenPlannerClassifiesPressureAndBudget(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		input     int64
		pressure  contextmanager.Pressure
		fits      bool
		remaining int64
		overflow  int64
	}{
		{name: "normal", input: 600, pressure: contextmanager.PressureNormal, fits: true, remaining: 250},
		{name: "warning", input: 700, pressure: contextmanager.PressureWarning, fits: true, remaining: 150},
		{name: "critical", input: 810, pressure: contextmanager.PressureCritical, fits: true, remaining: 40},
		{name: "overflow", input: 900, pressure: contextmanager.PressureOverflow, fits: false, overflow: 50},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			planner := newPlanner(t, agenttest.FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) {
				return test.input, nil
			}))
			plan, err := planner.Plan(context.Background(), validRequest())
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			if plan.Pressure != test.pressure || plan.Fits != test.fits || plan.RemainingInputTokens != test.remaining || plan.OverflowTokens != test.overflow {
				t.Fatalf("Plan = %#v", plan)
			}
			if plan.AvailableInputTokens != 850 || plan.RequiredTokens != test.input+150 {
				t.Fatalf("budget arithmetic = %#v", plan)
			}
		})
	}
}

func TestTokenPlannerCopiesCounterInput(t *testing.T) {
	t.Parallel()

	request := validRequest()
	originalText := request.Messages[0].Content[0].Text
	originalSchema := string(request.Tools[0].InputSchema)
	planner := newPlanner(t, agenttest.FuncTokenCounter(func(_ context.Context, value model.TokenRequest) (int64, error) {
		value.Messages[0].Content[0].Text = "mutated"
		value.Tools[0].InputSchema[0] = 'x'
		return 10, nil
	}))
	if _, err := planner.Plan(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content[0].Text != originalText || string(request.Tools[0].InputSchema) != originalSchema {
		t.Fatal("Counter mutated caller-owned Request")
	}
}

func TestTokenPlannerContainsCounterFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		fn   agenttest.FuncTokenCounter
		code contextmanager.ErrorCode
	}{
		{name: "error", fn: func(context.Context, model.TokenRequest) (int64, error) { return 0, errors.New("offline") }, code: contextmanager.ErrorCounterFailed},
		{name: "panic", fn: func(context.Context, model.TokenRequest) (int64, error) { panic("boom") }, code: contextmanager.ErrorCounterFailed},
		{name: "zero", fn: func(context.Context, model.TokenRequest) (int64, error) { return 0, nil }, code: contextmanager.ErrorInvalidCount},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := newPlanner(t, test.fn).Plan(context.Background(), validRequest())
			assertContextCode(t, err, test.code)
		})
	}
}

func TestTokenPlannerCancellationAndImpossibleBudget(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newPlanner(t, agenttest.FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) {
		t.Fatal("Counter called after cancellation")
		return 1, nil
	})).Plan(ctx, validRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Plan error = %v", err)
	}

	planner := newPlanner(t, agenttest.FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) { return 1, nil }))
	request := validRequest()
	request.OutputReserveTokens = 950
	_, err = planner.Plan(context.Background(), request)
	assertContextCode(t, err, contextmanager.ErrorImpossibleBudget)
}

func TestPlanValidationRejectsInconsistentValues(t *testing.T) {
	t.Parallel()

	planner := newPlanner(t, agenttest.FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) { return 600, nil }))
	plan, err := planner.Plan(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	plan.OverflowTokens = 1
	if err := plan.Validate(); err == nil {
		t.Fatal("Plan.Validate accepted inconsistent overflow")
	}
}

func TestTokenPlannerRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	counter := agenttest.FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) { return 1, nil })
	for _, options := range []contextmanager.TokenPlannerOptions{
		{},
		{Counter: counter, CountSource: "unknown", ContextWindowTokens: 1000},
		{Counter: counter, CountSource: contextmanager.CountSourceProvider},
		{Counter: counter, CountSource: contextmanager.CountSourceProvider, ContextWindowTokens: 1000, SafetyMarginTokens: 1000},
		{Counter: counter, CountSource: contextmanager.CountSourceProvider, ContextWindowTokens: 1000, WarningThresholdBasisPoints: 9500, CriticalThresholdBasisPoints: 9000},
	} {
		if _, err := contextmanager.NewTokenPlanner(options); err == nil {
			t.Fatalf("NewTokenPlanner accepted %#v", options)
		}
	}
}

func newPlanner(t *testing.T, counter model.TokenCounter) *contextmanager.TokenPlanner {
	t.Helper()
	planner, err := contextmanager.NewTokenPlanner(contextmanager.TokenPlannerOptions{
		Counter: counter, CountSource: contextmanager.CountSourceProvider,
		ContextWindowTokens: 1000, SafetyMarginTokens: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

func validRequest() contextmanager.Request {
	return contextmanager.Request{
		Model: model.Identity{Provider: "test", Model: "counter"},
		Messages: []message.Message{{
			ID: "msg_1", Role: message.RoleUser, Visibility: message.VisibilityPublic,
			Content: []message.Content{{Type: message.ContentText, Text: "hello"}},
		}},
		Tools:               []model.ToolDefinition{{Name: "lookup", InputSchema: []byte(`{"type":"object"}`)}},
		OutputReserveTokens: 100,
	}
}

func assertContextCode(t *testing.T, err error, code contextmanager.ErrorCode) {
	t.Helper()
	var value *contextmanager.Error
	if !errors.As(err, &value) || value.Code != code {
		t.Fatalf("error = %v, want Context code %q", err, code)
	}
}
