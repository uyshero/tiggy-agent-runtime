package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestRetryableModelFailureRecovers(t *testing.T) {
	t.Parallel()

	primary := agenttest.NewScriptedModel(
		agenttest.ModelStep{Err: providerFailure(model.ErrorUnavailable, true)},
		agenttest.ModelStep{Response: textResponse("recovered")},
	)
	value := newPolicyAgent(t, primary, agent.TransientModelFailurePolicy{
		MaxAttemptsPerTarget: 2,
	}, nil, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted || result.Iterations != 1 {
		t.Fatalf("status/iterations = %s/%d", result.Status, result.Iterations)
	}
	if got := len(primary.Calls()); got != 2 {
		t.Fatalf("primary calls = %d, want 2", got)
	}
	retry := findEvent(t, result.Events, event.RetryScheduled)
	payload, err := agenttest.DecodePayload[event.RetryScheduledPayload](retry)
	if err != nil {
		t.Fatal(err)
	}
	if payload.NextAttempt != 2 || payload.Reason != "retryable_provider_error" {
		t.Fatalf("retry payload = %#v", payload)
	}
	assertTerminalInvariants(t, result)
}

func TestFallbackModelActivatesAfterAttemptsExhausted(t *testing.T) {
	t.Parallel()

	primary := agenttest.NewScriptedModel(agenttest.ModelStep{Err: providerFailure(model.ErrorUnavailable, true)})
	fallback := agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("from fallback")})
	value := newPolicyAgent(t, primary, agent.TransientModelFailurePolicy{
		MaxAttemptsPerTarget: 1,
		EnableFallback:       true,
	}, []agent.ModelTarget{{
		Model: fallback, Identity: model.Identity{Provider: "backup", Model: "secondary"},
	}}, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if len(primary.Calls()) != 1 || len(fallback.Calls()) != 1 {
		t.Fatalf("primary/fallback calls = %d/%d", len(primary.Calls()), len(fallback.Calls()))
	}
	activated := findEvent(t, result.Events, event.FallbackActivated)
	payload, err := agenttest.DecodePayload[event.FallbackActivatedPayload](activated)
	if err != nil {
		t.Fatal(err)
	}
	if payload.From.Provider != "test" || payload.To.Provider != "backup" || payload.FallbackIndex != 1 {
		t.Fatalf("fallback payload = %#v", payload)
	}
	assertTerminalInvariants(t, result)
}

func TestNonRetryableFailureDoesNotUseFallback(t *testing.T) {
	t.Parallel()

	primary := agenttest.NewScriptedModel(agenttest.ModelStep{Err: providerFailure(model.ErrorAuthentication, false)})
	fallback := agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("must not run")})
	value := newPolicyAgent(t, primary, agent.TransientModelFailurePolicy{
		MaxAttemptsPerTarget: 3,
		EnableFallback:       true,
	}, []agent.ModelTarget{{
		Model: fallback, Identity: model.Identity{Provider: "backup", Model: "secondary"},
	}}, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("hello"))
	assertRunError(t, err, agent.ErrorModel, string(model.ErrorAuthentication))
	if got := len(fallback.Calls()); got != 0 {
		t.Fatalf("fallback calls = %d, want 0", got)
	}
	if countEvents(result.Events, event.RetryScheduled)+countEvents(result.Events, event.FallbackActivated) != 0 {
		t.Fatal("non-retryable error scheduled recovery")
	}
	assertTerminalInvariants(t, result)
}

func TestCancellationInterruptsRetryBackoff(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	primary := agenttest.NewScriptedModel(agenttest.ModelStep{Err: providerFailure(model.ErrorUnavailable, true)})
	value := newPolicyAgent(t, primary, agent.TransientModelFailurePolicy{
		MaxAttemptsPerTarget: 2,
		BaseDelay:            time.Hour,
	}, nil, agent.DefaultLimits())
	startedAt := time.Now()

	result, err := value.RunStream(ctx, userMessage("hello"), func(value event.Event) error {
		if value.Type == event.RetryScheduled {
			cancel()
		}
		return nil
	})
	assertRunError(t, err, agent.ErrorCanceled, "retry_canceled")
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("canceled retry took %s", elapsed)
	}
	if len(primary.Calls()) != 1 {
		t.Fatalf("primary calls = %d, want 1", len(primary.Calls()))
	}
	assertTerminalInvariants(t, result)
}

func TestModelFailurePolicyPanicIsContained(t *testing.T) {
	t.Parallel()

	primary := agenttest.NewScriptedModel(agenttest.ModelStep{Err: providerFailure(model.ErrorUnavailable, true)})
	value := newPolicyAgent(t, primary, panicFailurePolicy{}, nil, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("hello"))
	assertRunError(t, err, agent.ErrorInternal, "model_failure_policy_failed")
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	assertTerminalInvariants(t, result)
}

func TestFallbackRequiresPolicy(t *testing.T) {
	t.Parallel()

	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.New(agent.Config{
		Name: "invalid", Model: agenttest.NewScriptedModel(),
		ModelIdentity: model.Identity{Provider: "test", Model: "primary"},
		Fallbacks: []agent.ModelTarget{{
			Model: agenttest.NewScriptedModel(), Identity: model.Identity{Provider: "backup", Model: "secondary"},
		}},
		Toolkit: kit,
	})
	if err == nil {
		t.Fatal("New() accepted fallback models without a failure policy")
	}
}

type panicFailurePolicy struct{}

func (panicFailurePolicy) Decide(agent.ModelFailure) agent.ModelFailureDecision {
	panic("policy boom")
}

func newPolicyAgent(
	t *testing.T,
	primary model.Model,
	policy agent.ModelFailurePolicy,
	fallbacks []agent.ModelTarget,
	limits agent.Limits,
) *agent.Agent {
	t.Helper()
	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	value, err := agent.New(agent.Config{
		Name: "policy-test", Model: primary,
		ModelIdentity: model.Identity{Provider: "test", Model: "primary"},
		Fallbacks:     fallbacks, FailurePolicy: policy, Toolkit: kit, Limits: limits,
		IDs: agenttest.SequentialIDs(),
		Now: agenttest.StepClock(time.Unix(1_700_000_000, 0), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func providerFailure(class model.ErrorClass, retryable bool) error {
	return &model.ProviderError{Class: class, Retryable: retryable, SafeDetail: "provider failed", Cause: errors.New("test cause")}
}

func findEvent(t *testing.T, values []event.Event, eventType event.Type) event.Event {
	t.Helper()
	for _, value := range values {
		if value.Type == eventType {
			return value
		}
	}
	t.Fatalf("event %q not found", eventType)
	return event.Event{}
}

func countEvents(values []event.Event, eventType event.Type) int {
	count := 0
	for _, value := range values {
		if value.Type == eventType {
			count++
		}
	}
	return count
}
