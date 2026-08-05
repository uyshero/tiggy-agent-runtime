package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestAgentPlansContextBeforeModelCall(t *testing.T) {
	t.Parallel()

	planner := fixedPlanner(t, 600, 1000)
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")})
	value := newContextAgent(t, script, planner, 100)
	result, err := value.Run(context.Background(), userMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
	if err := agenttest.ValidateTrace(withoutModelContexts(t, result.Events)); err != nil {
		t.Fatalf("legacy model-call trace: %v", err)
	}
	if len(script.Calls()) != 1 {
		t.Fatalf("model calls = %d", len(script.Calls()))
	}
	prepared := findContextPayload(t, result.Events)
	if prepared.Status != "ready" || !prepared.Fits || prepared.InputTokens != 600 || prepared.AvailableInputTokens != 850 || prepared.RemainingInputTokens != 250 {
		t.Fatalf("context prepared = %#v", prepared)
	}
}

func TestAgentRejectsContextOverflowBeforeModelCall(t *testing.T) {
	t.Parallel()

	called := false
	provider := agenttest.FuncModel(func(context.Context, model.Request, model.DeltaSink) (model.Response, error) {
		called = true
		return model.Response{}, errors.New("must not be called")
	})
	value := newContextAgent(t, provider, fixedPlanner(t, 900, 1000), 100)
	result, err := value.Run(context.Background(), userMessage("too large"))
	assertRunError(t, err, agent.ErrorLimit, "context_overflow")
	if called {
		t.Fatal("model was called with overflowing context")
	}
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	prepared := findContextPayload(t, result.Events)
	if prepared.Status != "overflow" || prepared.OverflowTokens != 50 || prepared.Pressure != string(contextmanager.PressureOverflow) {
		t.Fatalf("context prepared = %#v", prepared)
	}
	if containsEventType(result.Events, event.ModelCallStarted) {
		t.Fatal("overflow trace contains model.call.started")
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestAgentContainsContextPlannerPanicAndInvalidPlan(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		planner contextmanager.Planner
		code    string
	}{
		{name: "panic", planner: plannerFunc(func(context.Context, contextmanager.Request) (contextmanager.Plan, error) { panic("boom") }), code: "context_planner_failed"},
		{name: "invalid", planner: plannerFunc(func(context.Context, contextmanager.Request) (contextmanager.Plan, error) {
			return contextmanager.Plan{}, nil
		}), code: "invalid_context_plan"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := newContextAgent(t, agenttest.NewScriptedModel(), test.planner, 100)
			result, err := value.Run(context.Background(), userMessage("hello"))
			assertRunError(t, err, agent.ErrorInternal, test.code)
			if err := agenttest.ValidateTrace(result.Events); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentContextPlannerCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	planner := plannerFunc(func(callCtx context.Context, _ contextmanager.Request) (contextmanager.Plan, error) {
		cancel()
		<-callCtx.Done()
		return contextmanager.Plan{}, callCtx.Err()
	})
	value := newContextAgent(t, agenttest.NewScriptedModel(), planner, 100)
	result, err := value.Run(ctx, userMessage("hello"))
	assertRunError(t, err, agent.ErrorCanceled, "context_prepare_canceled")
	if result.Status != agent.StatusCanceled {
		t.Fatalf("status = %q", result.Status)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackUsesItsOwnContextPlanner(t *testing.T) {
	t.Parallel()

	primaryPlans := 0
	fallbackPlans := 0
	primaryPlanner := countingPlanner(t, 10, 1000, &primaryPlans)
	fallbackPlanner := countingPlanner(t, 20, 2000, &fallbackPlans)
	primary := agenttest.NewScriptedModel(agenttest.ModelStep{Err: &model.ProviderError{
		Class: model.ErrorUnavailable, SafeDetail: "unavailable", Retryable: true,
	}})
	fallback := agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("fallback")})
	limits := agent.DefaultLimits()
	limits.MaxOutputTokensPerCall = 100
	value, err := agent.New(agent.Config{
		Name: "fallback-context", Model: primary, ModelIdentity: model.Identity{Provider: "test", Model: "primary"},
		ContextPlanner: primaryPlanner,
		Fallbacks: []agent.ModelTarget{{
			Model: fallback, Identity: model.Identity{Provider: "test", Model: "fallback"}, ContextPlanner: fallbackPlanner,
		}},
		FailurePolicy: agent.TransientModelFailurePolicy{MaxAttemptsPerTarget: 1, EnableFallback: true},
		Limits:        limits, IDs: agenttest.SequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := value.Run(context.Background(), userMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if primaryPlans != 1 || fallbackPlans != 1 {
		t.Fatalf("planner calls = primary %d, fallback %d", primaryPlans, fallbackPlans)
	}
	if countEventType(result.Events, event.ContextPrepareStarted) != 2 {
		t.Fatalf("context prepare events = %#v", agenttest.EventTypes(result.Events))
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackRequiresConsistentContextPlannerConfiguration(t *testing.T) {
	t.Parallel()

	primary := agenttest.NewScriptedModel()
	fallback := agenttest.NewScriptedModel()
	planner := fixedPlanner(t, 10, 1000)
	for _, config := range []agent.Config{
		{
			Name: "missing-fallback-planner", Model: primary, ModelIdentity: model.Identity{Provider: "test", Model: "primary"},
			ContextPlanner: planner,
			Fallbacks:      []agent.ModelTarget{{Model: fallback, Identity: model.Identity{Provider: "test", Model: "fallback"}}},
			FailurePolicy:  agent.TransientModelFailurePolicy{EnableFallback: true},
		},
		{
			Name: "missing-primary-planner", Model: primary, ModelIdentity: model.Identity{Provider: "test", Model: "primary"},
			Fallbacks: []agent.ModelTarget{{
				Model: fallback, Identity: model.Identity{Provider: "test", Model: "fallback"}, ContextPlanner: planner,
			}},
			FailurePolicy: agent.TransientModelFailurePolicy{EnableFallback: true},
		},
	} {
		if _, err := agent.New(config); err == nil {
			t.Fatalf("agent.New accepted inconsistent planners: %#v", config)
		}
	}
}

func TestAgentCompressesOverflowingContextWithoutMutatingState(t *testing.T) {
	t.Parallel()

	planner := messageCountPlanner(t, func(messages int) int64 {
		if messages > 2 {
			return 900
		}
		return 400
	})
	compressor := agenttest.FuncCompressor(func(_ context.Context, request contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		if len(request.Groups) != 1 || len(request.Groups[0].Messages) != 2 {
			return contextmanager.CompressionDecision{}, errors.New("unexpected semantic groups")
		}
		return contextmanager.CompressionDecision{
			Strategy:      contextmanager.CompressionStrategy{Name: "test-summary", Version: "1"},
			CoveredGroups: 1, Summary: "Earlier, the user asked an old question and the assistant answered it.",
		}, nil
	})
	script := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			if len(request.Messages) != 2 || request.Messages[0].Role != message.RoleSystem || request.Messages[1].Role != message.RoleUser {
				return errors.New("model did not receive summary plus active turn")
			}
			return nil
		},
		Response: textResponse("done"),
	})
	value := newCompressionAgent(t, script, planner, compressor)
	previous := agent.State{Messages: []message.Message{
		{ID: "old_user", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "old question"}}},
		{ID: "old_assistant", Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "old answer"}}},
	}}
	result, err := value.Continue(context.Background(), previous, userMessage("new question"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.State.Messages) != 4 || result.State.Messages[0].ID != "old_user" || result.State.Messages[1].ID != "old_assistant" {
		t.Fatalf("durable state was replaced by compressed view: %#v", result.State.Messages)
	}
	for _, value := range result.State.Messages {
		if value.Role == message.RoleSystem {
			t.Fatalf("compression summary leaked into full State: %#v", value)
		}
	}
	if countEventType(result.Events, event.ContextPrepareStarted) != 2 || countEventType(result.Events, event.CompressionCompleted) != 1 {
		t.Fatalf("compression events = %#v", agenttest.EventTypes(result.Events))
	}
	payload := findCompressionPayload(t, result.Events, event.CompressionCompleted)
	if payload.Status != "completed" || payload.BeforeInputTokens != 900 || payload.AfterInputTokens != 400 || payload.Fallback != "none" || len(payload.CoveredMessageIDs) != 2 {
		t.Fatalf("compression payload = %#v", payload)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
	if err := agenttest.ValidateTrace(withoutCompressionTriggers(t, result.Events)); err != nil {
		t.Fatalf("legacy compression trace: %v", err)
	}
}

func TestAgentFailsClosedWhenCompressionIsInsufficient(t *testing.T) {
	t.Parallel()

	called := false
	provider := agenttest.FuncModel(func(context.Context, model.Request, model.DeltaSink) (model.Response, error) {
		called = true
		return model.Response{}, errors.New("must not be called")
	})
	value := newCompressionAgent(t, provider, messageCountPlanner(t, func(int) int64 { return 900 }), validCompressor())
	previous := compressionPreviousState()
	result, err := value.Continue(context.Background(), previous, userMessage("new question"), nil)
	assertRunError(t, err, agent.ErrorLimit, "context_overflow")
	if called {
		t.Fatal("model was called after insufficient compression")
	}
	payload := findCompressionPayload(t, result.Events, event.CompressionCompleted)
	if payload.Status != "insufficient" || payload.Fallback != "fail_closed" || payload.AfterInputTokens != 900 {
		t.Fatalf("compression payload = %#v", payload)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestAgentContainsInvalidCompressionAndPanic(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		compressor contextmanager.Compressor
		code       string
	}{
		{name: "invalid", compressor: agenttest.FuncCompressor(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
			return contextmanager.CompressionDecision{}, nil
		}), code: "invalid_compression_result"},
		{name: "panic", compressor: agenttest.FuncCompressor(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
			panic("boom")
		}), code: "context_compression_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := newCompressionAgent(t, agenttest.NewScriptedModel(), messageCountPlanner(t, func(int) int64 { return 900 }), test.compressor)
			result, err := value.Continue(context.Background(), compressionPreviousState(), userMessage("new question"), nil)
			assertRunError(t, err, agent.ErrorInternal, test.code)
			if countEventType(result.Events, event.CompressionFailed) != 1 {
				t.Fatalf("compression events = %#v", agenttest.EventTypes(result.Events))
			}
			if err := agenttest.ValidateTrace(result.Events); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentCompressionCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	compressor := agenttest.FuncCompressor(func(callCtx context.Context, _ contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		cancel()
		<-callCtx.Done()
		return contextmanager.CompressionDecision{}, callCtx.Err()
	})
	value := newCompressionAgent(t, agenttest.NewScriptedModel(), messageCountPlanner(t, func(int) int64 { return 900 }), compressor)
	result, err := value.Continue(ctx, compressionPreviousState(), userMessage("new question"), nil)
	assertRunError(t, err, agent.ErrorCanceled, "context_compression_canceled")
	if result.Status != agent.StatusCanceled {
		t.Fatalf("status = %q", result.Status)
	}
	payload := findCompressionPayload(t, result.Events, event.CompressionFailed)
	if payload.Status != "canceled" || payload.Fallback != "fail_closed" {
		t.Fatalf("compression payload = %#v", payload)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCompressionEventSinkFailureKeepsBalancedTrace(t *testing.T) {
	t.Parallel()

	value := newCompressionAgent(t, agenttest.NewScriptedModel(), messageCountPlanner(t, func(int) int64 { return 900 }), validCompressor())
	result, err := value.Continue(context.Background(), compressionPreviousState(), userMessage("new question"), func(value event.Event) error {
		if value.Type == event.CompressionStarted {
			return errors.New("sink offline")
		}
		return nil
	})
	assertRunError(t, err, agent.ErrorEventSink, "event_sink_failed")
	if countEventType(result.Events, event.CompressionStarted) != 1 || countEventType(result.Events, event.CompressionFailed) != 1 {
		t.Fatalf("compression events = %#v", agenttest.EventTypes(result.Events))
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestProviderContextLengthRecoveryCompressesRequestViewOnce(t *testing.T) {
	t.Parallel()

	compressions := 0
	provider := agenttest.NewScriptedModel(
		agenttest.ModelStep{Err: &model.ProviderError{
			Class: model.ErrorContextLength, Code: "context_length_exceeded", SafeDetail: "context too long",
		}},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				if len(request.Messages) != 2 || request.Messages[0].Role != message.RoleSystem || request.Messages[0].Content[0].Text != "recovered history" || request.Messages[1].Role != message.RoleUser {
					return errors.New("recovery retry did not use the compressed request view")
				}
				return nil
			},
			Response: textResponse("recovered"),
		},
	)
	value := newProviderRecoveryAgent(t, provider, &compressions)
	result, err := value.Continue(context.Background(), compressionPreviousState(), userMessage("new question"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.Calls()) != 2 || compressions != 1 {
		t.Fatalf("Provider/compressor calls = %d/%d, want 2/1", len(provider.Calls()), compressions)
	}
	contexts := modelContextPayloads(t, result.Events)
	if len(contexts) != 2 || contexts[0].ProjectedMessageCount != 3 || contexts[0].RequestMessageCount != 3 || contexts[0].CompressionSummaries != 0 ||
		contexts[1].ProjectedMessageCount != 3 || contexts[1].RequestMessageCount != 2 || contexts[1].CompressionSummaries != 1 {
		t.Fatalf("model context snapshots = %#v", contexts)
	}
	if len(result.State.Messages) != 4 {
		t.Fatalf("State messages = %d, want complete uncompressed history", len(result.State.Messages))
	}
	for _, value := range result.State.Messages {
		if value.Role == message.RoleSystem {
			t.Fatalf("recovery summary leaked into State: %#v", value)
		}
	}
	payload := findCompressionPayload(t, result.Events, event.CompressionCompleted)
	if payload.Trigger != string(contextmanager.CompressionTriggerProviderContextLength) || payload.OverflowTokens != 0 || payload.BeforeInputTokens != 800 || payload.AfterInputTokens != 400 {
		t.Fatalf("recovery compression payload = %#v", payload)
	}
	retryEvent := findEvent(t, result.Events, event.RetryScheduled)
	retry, err := agenttest.DecodePayload[event.RetryScheduledPayload](retryEvent)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Reason != "context_length_recovery" || retry.NextAttempt != 2 || retry.DelayMillis != 0 {
		t.Fatalf("recovery retry payload = %#v", retry)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestProviderContextLengthRecoveryDoesNotRepeatForTarget(t *testing.T) {
	t.Parallel()

	compressions := 0
	provider := agenttest.NewScriptedModel(
		agenttest.ModelStep{Err: &model.ProviderError{Class: model.ErrorContextLength, Code: "first", SafeDetail: "context too long"}},
		agenttest.ModelStep{Err: &model.ProviderError{Class: model.ErrorContextLength, Code: "second", SafeDetail: "still too long"}},
	)
	value := newProviderRecoveryAgent(t, provider, &compressions)
	result, err := value.Continue(context.Background(), compressionPreviousState(), userMessage("new question"), nil)
	assertRunError(t, err, agent.ErrorModel, string(model.ErrorContextLength))
	if len(provider.Calls()) != 2 || compressions != 1 || countEventType(result.Events, event.RetryScheduled) != 1 || countEventType(result.Events, event.CompressionCompleted) != 1 {
		t.Fatalf("calls/events = Provider %d, compressor %d, retries %d, compressions %d",
			len(provider.Calls()), compressions, countEventType(result.Events, event.RetryScheduled), countEventType(result.Events, event.CompressionCompleted))
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackRecompressesFullStateWithItsOwnCompressor(t *testing.T) {
	t.Parallel()

	primaryCompressions := 0
	fallbackCompressions := 0
	primaryCompressor := agenttest.FuncCompressor(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		primaryCompressions++
		return contextmanager.CompressionDecision{
			Strategy:      contextmanager.CompressionStrategy{Name: "primary-summary", Version: "1"},
			CoveredGroups: 1, Summary: "primary history",
		}, nil
	})
	fallbackCompressor := agenttest.FuncCompressor(func(_ context.Context, request contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		fallbackCompressions++
		if request.Groups[0].Messages[0].ID != "old_user" {
			return contextmanager.CompressionDecision{}, errors.New("fallback did not receive full source history")
		}
		return contextmanager.CompressionDecision{
			Strategy:      contextmanager.CompressionStrategy{Name: "fallback-summary", Version: "1"},
			CoveredGroups: 1, Summary: "fallback history",
		}, nil
	})
	primary := agenttest.NewScriptedModel(agenttest.ModelStep{Err: &model.ProviderError{
		Class: model.ErrorUnavailable, SafeDetail: "unavailable", Retryable: true,
	}})
	fallback := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			if len(request.Messages) != 2 || request.Messages[0].Content[0].Text != "fallback history" {
				return errors.New("fallback received the wrong compressed view")
			}
			return nil
		},
		Response: textResponse("done"),
	})
	limits := agent.DefaultLimits()
	limits.MaxOutputTokensPerCall = 100
	value, err := agent.New(agent.Config{
		Name: "fallback-compression", Model: primary, ModelIdentity: model.Identity{Provider: "test", Model: "primary"},
		ContextPlanner: messageCountPlanner(t, func(messages int) int64 {
			if messages > 2 {
				return 900
			}
			return 400
		}),
		ContextCompressor: primaryCompressor,
		Fallbacks: []agent.ModelTarget{{
			Model: fallback, Identity: model.Identity{Provider: "test", Model: "fallback"},
			ContextPlanner: messageCountPlanner(t, func(messages int) int64 {
				if messages > 2 {
					return 900
				}
				return 400
			}),
			ContextCompressor: fallbackCompressor,
		}},
		FailurePolicy: agent.TransientModelFailurePolicy{MaxAttemptsPerTarget: 1, EnableFallback: true},
		Limits:        limits, IDs: agenttest.SequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := value.Continue(context.Background(), compressionPreviousState(), userMessage("new question"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if primaryCompressions != 1 || fallbackCompressions != 1 || countEventType(result.Events, event.CompressionCompleted) != 2 {
		t.Fatalf("compression counts = primary %d, fallback %d; events %#v", primaryCompressions, fallbackCompressions, agenttest.EventTypes(result.Events))
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestCompressionRequiresContextPlanner(t *testing.T) {
	t.Parallel()

	_, err := agent.New(agent.Config{
		Name: "invalid-compression", Model: agenttest.NewScriptedModel(),
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"}, ContextCompressor: validCompressor(),
	})
	if err == nil {
		t.Fatal("agent.New accepted ContextCompressor without ContextPlanner")
	}
}

type plannerFunc func(context.Context, contextmanager.Request) (contextmanager.Plan, error)

func (f plannerFunc) Plan(ctx context.Context, request contextmanager.Request) (contextmanager.Plan, error) {
	return f(ctx, request)
}

func fixedPlanner(t *testing.T, tokens, window int64) *contextmanager.TokenPlanner {
	t.Helper()
	return countingPlanner(t, tokens, window, nil)
}

func countingPlanner(t *testing.T, tokens, window int64, calls *int) *contextmanager.TokenPlanner {
	t.Helper()
	planner, err := contextmanager.NewTokenPlanner(contextmanager.TokenPlannerOptions{
		Counter: agenttest.FuncTokenCounter(func(context.Context, model.TokenRequest) (int64, error) {
			if calls != nil {
				(*calls)++
			}
			return tokens, nil
		}),
		CountSource: contextmanager.CountSourceProvider, ContextWindowTokens: window, SafetyMarginTokens: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

func newContextAgent(t *testing.T, provider model.Model, planner contextmanager.Planner, outputReserve int) *agent.Agent {
	t.Helper()
	limits := agent.DefaultLimits()
	limits.MaxOutputTokensPerCall = outputReserve
	value, err := agent.New(agent.Config{
		Name: "context-test", Model: provider, ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		ContextPlanner: planner, Limits: limits, IDs: agenttest.SequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newCompressionAgent(t *testing.T, provider model.Model, planner contextmanager.Planner, compressor contextmanager.Compressor) *agent.Agent {
	t.Helper()
	limits := agent.DefaultLimits()
	limits.MaxOutputTokensPerCall = 100
	value, err := agent.New(agent.Config{
		Name: "compression-test", Model: provider, ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		ContextPlanner: planner, ContextCompressor: compressor, Limits: limits, IDs: agenttest.SequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newProviderRecoveryAgent(t *testing.T, provider model.Model, compressions *int) *agent.Agent {
	t.Helper()
	limits := agent.DefaultLimits()
	limits.MaxOutputTokensPerCall = 100
	compressor := agenttest.FuncCompressor(func(_ context.Context, request contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		(*compressions)++
		if request.Trigger != contextmanager.CompressionTriggerProviderContextLength || !request.Plan.Fits || request.Plan.OverflowTokens != 0 {
			return contextmanager.CompressionDecision{}, errors.New("invalid Provider recovery request")
		}
		return contextmanager.CompressionDecision{
			Strategy:      contextmanager.CompressionStrategy{Name: "provider-recovery", Version: "1"},
			CoveredGroups: 1, Summary: "recovered history",
		}, nil
	})
	value, err := agent.New(agent.Config{
		Name: "provider-context-recovery", Model: provider,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		ContextPlanner: messageCountPlanner(t, func(messages int) int64 {
			if messages > 2 {
				return 800
			}
			return 400
		}),
		ContextCompressor: compressor,
		FailurePolicy: agent.TransientModelFailurePolicy{
			MaxAttemptsPerTarget: 1, EnableContextRecovery: true,
		},
		Limits: limits, IDs: agenttest.SequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func messageCountPlanner(t *testing.T, count func(int) int64) *contextmanager.TokenPlanner {
	t.Helper()
	planner, err := contextmanager.NewTokenPlanner(contextmanager.TokenPlannerOptions{
		Counter: agenttest.FuncTokenCounter(func(_ context.Context, request model.TokenRequest) (int64, error) {
			return count(len(request.Messages)), nil
		}),
		CountSource: contextmanager.CountSourceEstimated, ContextWindowTokens: 1000, SafetyMarginTokens: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

func validCompressor() contextmanager.Compressor {
	return agenttest.FuncCompressor(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		return contextmanager.CompressionDecision{
			Strategy:      contextmanager.CompressionStrategy{Name: "test-summary", Version: "1"},
			CoveredGroups: 1, Summary: "Summary of earlier context.",
		}, nil
	})
}

func compressionPreviousState() agent.State {
	return agent.State{Messages: []message.Message{
		{ID: "old_user", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "old question"}}},
		{ID: "old_assistant", Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "old answer"}}},
	}}
}

func findCompressionPayload(t *testing.T, events []event.Event, eventType event.Type) event.CompressionPayload {
	t.Helper()
	for _, value := range events {
		if value.Type == eventType {
			payload, err := agenttest.DecodePayload[event.CompressionPayload](value)
			if err != nil {
				t.Fatal(err)
			}
			return payload
		}
	}
	t.Fatalf("%s event not found", eventType)
	return event.CompressionPayload{}
}

func findContextPayload(t *testing.T, events []event.Event) event.ContextPreparePayload {
	t.Helper()
	for _, value := range events {
		if value.Type == event.ContextPrepared {
			payload, err := agenttest.DecodePayload[event.ContextPreparePayload](value)
			if err != nil {
				t.Fatal(err)
			}
			return payload
		}
	}
	t.Fatal("context.prepared event not found")
	return event.ContextPreparePayload{}
}

func modelContextPayloads(t *testing.T, events []event.Event) []event.ModelContextPayload {
	t.Helper()
	var payloads []event.ModelContextPayload
	for _, value := range events {
		if value.Type != event.ModelCallStarted {
			continue
		}
		payload, err := agenttest.DecodePayload[event.ModelCallPayload](value)
		if err != nil {
			t.Fatal(err)
		}
		if payload.Context == nil {
			t.Fatal("model.call.started has no context snapshot")
		}
		payloads = append(payloads, *payload.Context)
	}
	return payloads
}

func withoutCompressionTriggers(t *testing.T, events []event.Event) []event.Event {
	t.Helper()
	legacy := append([]event.Event(nil), events...)
	removed := 0
	for index := range legacy {
		if legacy[index].Type != event.CompressionStarted && legacy[index].Type != event.CompressionCompleted && legacy[index].Type != event.CompressionFailed {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(legacy[index].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		delete(payload, "trigger")
		removed++
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		legacy[index].Payload = encoded
	}
	if removed == 0 {
		t.Fatal("trace contains no compression events")
	}
	return legacy
}

func withoutModelContexts(t *testing.T, events []event.Event) []event.Event {
	t.Helper()
	legacy := append([]event.Event(nil), events...)
	removed := 0
	for index := range legacy {
		if legacy[index].Type != event.ModelCallStarted {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(legacy[index].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		delete(payload, "context")
		removed++
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		legacy[index].Payload = encoded
	}
	if removed == 0 {
		t.Fatal("trace contains no Model Call start events")
	}
	return legacy
}

func containsEventType(events []event.Event, eventType event.Type) bool {
	return countEventType(events, eventType) > 0
}

func countEventType(events []event.Event, eventType event.Type) int {
	count := 0
	for _, value := range events {
		if value.Type == eventType {
			count++
		}
	}
	return count
}
