package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestModelDeltaCountLimitBoundsProviderThatIgnoresSinkErrors(t *testing.T) {
	t.Parallel()

	provider := agenttest.FuncModel(func(_ context.Context, _ model.Request, sink model.DeltaSink) (model.Response, error) {
		for index := 0; index < 100; index++ {
			_ = sink(model.Delta{Type: model.DeltaText, Index: index, Text: "chunk"})
		}
		return textResponse("must not commit"), nil
	})
	limits := agent.DefaultLimits()
	limits.MaxModelDeltasPerCall = 2
	value := newAgent(t, provider, nil, limits)

	result, err := value.Run(context.Background(), userMessage("hello"))
	assertRunError(t, err, agent.ErrorLimit, "max_model_deltas")
	if got := countEvents(result.Events, event.ModelCallDelta); got != 2 {
		t.Fatalf("model Delta events = %d, want 2", got)
	}
	if result.Final != nil {
		t.Fatal("response was committed after the model stream exceeded its limit")
	}
	assertTerminalInvariants(t, result)
}

func TestModelDeltaByteLimitRejectsOversizedDelta(t *testing.T) {
	t.Parallel()

	provider := agenttest.NewScriptedModel(agenttest.ModelStep{
		Deltas:   []model.Delta{{Type: model.DeltaText, Index: 0, Text: "oversized"}},
		Response: textResponse("must not commit"),
	})
	limits := agent.DefaultLimits()
	limits.MaxModelDeltaBytesPerCall = 1
	value := newAgent(t, provider, nil, limits)

	result, err := value.Run(context.Background(), userMessage("hello"))
	assertRunError(t, err, agent.ErrorLimit, "max_model_delta_bytes")
	if got := countEvents(result.Events, event.ModelCallDelta); got != 0 {
		t.Fatalf("model Delta events = %d, want 0", got)
	}
	assertTerminalInvariants(t, result)
}

func TestLegacyExplicitLimitsReceiveModelDeltaDefaults(t *testing.T) {
	t.Parallel()

	limits := agent.Limits{
		MaxIterations:          2,
		MaxModelCalls:          2,
		MaxToolCalls:           2,
		MaxInputTokens:         1_000,
		MaxOutputTokens:        1_000,
		MaxReasoningTokens:     1_000,
		MaxOutputTokensPerCall: 128,
		MaxDuration:            time.Minute,
	}
	provider := agenttest.NewScriptedModel(agenttest.ModelStep{
		Deltas:   []model.Delta{{Type: model.DeltaText, Index: 0, Text: "done"}},
		Response: textResponse("done"),
	})
	value := newAgent(t, provider, nil, limits)

	result, err := value.Run(context.Background(), userMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted || countEvents(result.Events, event.ModelCallDelta) != 1 {
		t.Fatalf("status/delta events = %s/%d", result.Status, countEvents(result.Events, event.ModelCallDelta))
	}
}

func TestNegativeModelDeltaLimitsAreRejected(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*agent.Limits)
	}{
		{name: "count", mutate: func(limits *agent.Limits) { limits.MaxModelDeltasPerCall = -1 }},
		{name: "bytes", mutate: func(limits *agent.Limits) { limits.MaxModelDeltaBytesPerCall = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			limits := agent.DefaultLimits()
			test.mutate(&limits)
			_, err := agent.New(agent.Config{
				Name:          "invalid-stream-limit",
				Model:         agenttest.NewScriptedModel(),
				ModelIdentity: model.Identity{Provider: "test", Model: "fake"},
				Limits:        limits,
			})
			if err == nil {
				t.Fatal("agent.New accepted a negative model Delta limit")
			}
		})
	}
}

func TestConcurrentModelDeltaCallbacksAreSerialized(t *testing.T) {
	t.Parallel()

	const deltaCount = 32
	provider := agenttest.FuncModel(func(_ context.Context, _ model.Request, sink model.DeltaSink) (model.Response, error) {
		var wait sync.WaitGroup
		errs := make(chan error, deltaCount)
		for index := 0; index < deltaCount; index++ {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				if err := sink(model.Delta{Type: model.DeltaText, Index: index, Text: "chunk"}); err != nil {
					errs <- err
				}
			}(index)
		}
		wait.Wait()
		close(errs)
		for err := range errs {
			return model.Response{}, err
		}
		return textResponse("done"), nil
	})
	value := newAgent(t, provider, nil, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if got := countEvents(result.Events, event.ModelCallDelta); got != deltaCount {
		t.Fatalf("model Delta events = %d, want %d", got, deltaCount)
	}
	assertTerminalInvariants(t, result)
}

func TestModelDeltaCallbackIsClosedAfterGenerateReturns(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	lateResult := make(chan error, 1)
	provider := agenttest.FuncModel(func(_ context.Context, _ model.Request, sink model.DeltaSink) (model.Response, error) {
		go func() {
			<-release
			lateResult <- sink(model.Delta{Type: model.DeltaText, Index: 0, Text: "late"})
		}()
		return textResponse("done"), nil
	})
	value := newAgent(t, provider, nil, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("hello"))
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case lateErr := <-lateResult:
		if lateErr == nil || !strings.Contains(lateErr.Error(), "after Generate returned") {
			t.Fatalf("late DeltaSink error = %v", lateErr)
		}
	case <-time.After(time.Second):
		t.Fatal("late DeltaSink callback did not return")
	}
	if got := countEvents(result.Events, event.ModelCallDelta); got != 0 {
		t.Fatalf("model Delta events = %d, want 0", got)
	}
	assertTerminalInvariants(t, result)
}
