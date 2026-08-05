package reliability

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/memory"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

var evaluationIdentity = model.Identity{Provider: "agenttest", Model: "reliability-script"}

func scenarios() []scenario {
	return []scenario{
		successScenario(),
		modelPanicScenario(),
		invalidModelResponseScenario(),
		modelDeltaOverflowScenario(),
		contextOverflowScenario(),
		providerContextRecoveryScenario(),
		repeatedProviderContextFailureScenario(),
		compressorPanicScenario(),
		memoryRecallFailureScenario(),
		toolPanicScenario(),
		toolCancellationScenario(),
		invalidToolResultScenario(),
		eventSinkFailureScenario(),
	}
}

func providerContextRecoveryScenario() scenario {
	return providerContextScenario(false)
}

func repeatedProviderContextFailureScenario() scenario {
	return providerContextScenario(true)
}

func providerContextScenario(repeated bool) scenario {
	id := "provider-context-length-recovery"
	fault := "Provider rejects a request that passed local context preflight"
	expected := Outcome{Status: string(agent.StatusCompleted)}
	required := []event.Type{
		event.ModelCallStarted, event.ModelCallCompleted, event.CompressionStarted,
		event.CompressionCompleted, event.RetryScheduled, event.RunCompleted,
	}
	if repeated {
		id = "repeated-provider-context-length"
		fault = "Provider rejects both the original and single compressed retry"
		expected = failed(agent.ErrorModel, string(model.ErrorContextLength))
		required = append(required[:len(required)-1], event.RuntimeError, event.RunFailed)
	}
	return scenario{
		id: id, category: "context", fault: fault, expected: expected,
		requiredEvents: required,
		expectedMaxDelegates: map[string]int{
			"model": 2, "token_counter": 2, "compressor": 1,
		},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			provider := agenttest.FuncModel(func(_ context.Context, request model.Request, _ model.DeltaSink) (model.Response, error) {
				calls["model"]++
				if calls["model"] == 1 || repeated {
					return model.Response{}, &model.ProviderError{
						Class: model.ErrorContextLength, Code: "context_length_exceeded", SafeDetail: "context too long",
					}
				}
				if len(request.Messages) != 2 || request.Messages[0].Role != message.RoleSystem {
					return model.Response{}, errors.New("compressed recovery view was not used")
				}
				return textResponse("recovered"), nil
			})
			planner, err := tokenPlanner(calls, func(request model.TokenRequest) int64 {
				if len(request.Messages) > 2 {
					return 800
				}
				return 400
			})
			if err != nil {
				return observation{delegateCalls: calls}, err
			}
			compressor := agenttest.FuncCompressor(func(_ context.Context, request contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
				calls["compressor"]++
				if request.Trigger != contextmanager.CompressionTriggerProviderContextLength || !request.Plan.Fits {
					return contextmanager.CompressionDecision{}, errors.New("invalid context recovery request")
				}
				return contextmanager.CompressionDecision{
					Strategy:      contextmanager.CompressionStrategy{Name: "reliability-recovery", Version: "1"},
					CoveredGroups: 1, Summary: "bounded summary",
				}, nil
			})
			limits := agent.DefaultLimits()
			limits.MaxOutputTokensPerCall = 100
			config := agent.Config{
				Model: provider, ContextPlanner: planner, ContextCompressor: compressor,
				FailurePolicy: agent.TransientModelFailurePolicy{EnableContextRecovery: true}, Limits: limits,
			}
			previous := historicalState()
			return execute(ctx, config, &previous, nil, calls)
		},
	}
}

func successScenario() scenario {
	return scenario{
		id: "successful-completion", category: "control", fault: "none",
		expected:             Outcome{Status: string(agent.StatusCompleted)},
		requiredEvents:       []event.Type{event.ModelCallStarted, event.ModelCallCompleted, event.RunCompleted},
		expectedMaxDelegates: map[string]int{"model": 1},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			provider := agenttest.FuncModel(func(context.Context, model.Request, model.DeltaSink) (model.Response, error) {
				calls["model"]++
				return textResponse("completed"), nil
			})
			return execute(ctx, agent.Config{Model: provider}, nil, nil, calls)
		},
	}
}

func modelPanicScenario() scenario {
	return scenario{
		id: "model-panic", category: "model", fault: "model adapter panics",
		expected:             failed(agent.ErrorModel, "model_failed"),
		requiredEvents:       []event.Type{event.ModelCallStarted, event.ModelCallCompleted, event.RuntimeError, event.RunFailed},
		expectedMaxDelegates: map[string]int{"model": 1},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			provider := agenttest.FuncModel(func(context.Context, model.Request, model.DeltaSink) (model.Response, error) {
				calls["model"]++
				panic("injected model panic")
			})
			return execute(ctx, agent.Config{Model: provider}, nil, nil, calls)
		},
	}
}

func invalidModelResponseScenario() scenario {
	return scenario{
		id: "invalid-model-response", category: "model", fault: "model returns an empty response",
		expected:             failed(agent.ErrorModel, "invalid_model_response"),
		requiredEvents:       []event.Type{event.ModelCallStarted, event.ModelCallCompleted, event.RuntimeError, event.RunFailed},
		expectedMaxDelegates: map[string]int{"model": 1},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			provider := agenttest.FuncModel(func(context.Context, model.Request, model.DeltaSink) (model.Response, error) {
				calls["model"]++
				return model.Response{}, nil
			})
			return execute(ctx, agent.Config{Model: provider}, nil, nil, calls)
		},
	}
}

func modelDeltaOverflowScenario() scenario {
	return scenario{
		id: "model-delta-overflow", category: "limit", fault: "model emits more Deltas than the per-call limit",
		expected:             failed(agent.ErrorLimit, "max_model_deltas"),
		requiredEvents:       []event.Type{event.ModelCallStarted, event.ModelCallDelta, event.ModelCallCompleted, event.RuntimeError, event.RunFailed},
		expectedMaxDelegates: map[string]int{"model": 1},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			provider := agenttest.FuncModel(func(_ context.Context, _ model.Request, sink model.DeltaSink) (model.Response, error) {
				calls["model"]++
				for index := 0; index < 3; index++ {
					if err := sink(model.Delta{Type: model.DeltaText, Index: index, Text: "x"}); err != nil {
						return model.Response{}, err
					}
				}
				return textResponse("unreachable"), nil
			})
			limits := agent.DefaultLimits()
			limits.MaxModelDeltasPerCall = 2
			return execute(ctx, agent.Config{Model: provider, Limits: limits}, nil, nil, calls)
		},
	}
}

func contextOverflowScenario() scenario {
	return scenario{
		id: "context-overflow", category: "context", fault: "planned context exceeds the available input budget",
		expected:       failed(agent.ErrorLimit, "context_overflow"),
		requiredEvents: []event.Type{event.ContextPrepareStarted, event.ContextPrepared, event.RuntimeError, event.RunFailed},
		expectedMaxDelegates: map[string]int{
			"model": 0, "token_counter": 1,
		},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			provider := countingTextModel(calls)
			planner, err := tokenPlanner(calls, func(model.TokenRequest) int64 { return 900 })
			if err != nil {
				return observation{delegateCalls: calls}, err
			}
			limits := agent.DefaultLimits()
			limits.MaxOutputTokensPerCall = 100
			return execute(ctx, agent.Config{Model: provider, ContextPlanner: planner, Limits: limits}, nil, nil, calls)
		},
	}
}

func compressorPanicScenario() scenario {
	return scenario{
		id: "compressor-panic", category: "context", fault: "context Compressor panics",
		expected:       failed(agent.ErrorInternal, "context_compression_failed"),
		requiredEvents: []event.Type{event.ContextPrepareStarted, event.ContextPrepared, event.CompressionStarted, event.CompressionFailed, event.RuntimeError, event.RunFailed},
		expectedMaxDelegates: map[string]int{
			"model": 0, "token_counter": 1, "compressor": 1,
		},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			planner, err := tokenPlanner(calls, func(model.TokenRequest) int64 { return 900 })
			if err != nil {
				return observation{delegateCalls: calls}, err
			}
			compressor := agenttest.FuncCompressor(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
				calls["compressor"]++
				panic("injected compressor panic")
			})
			limits := agent.DefaultLimits()
			limits.MaxOutputTokensPerCall = 100
			config := agent.Config{Model: countingTextModel(calls), ContextPlanner: planner, ContextCompressor: compressor, Limits: limits}
			previous := historicalState()
			return execute(ctx, config, &previous, nil, calls)
		},
	}
}

func memoryRecallFailureScenario() scenario {
	return scenario{
		id: "memory-recall-failure", category: "memory", fault: "Memory recall returns an adapter error in fail-run mode",
		expected:       failed(agent.ErrorMemory, "memory_recall_failed"),
		requiredEvents: []event.Type{event.MemoryRecallStarted, event.MemoryRecallCompleted, event.RuntimeError, event.RunFailed},
		expectedMaxDelegates: map[string]int{
			"memory_recall": 1, "memory_observe": 0, "model": 0,
		},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			port := agenttest.FuncMemory{
				RecallFunc: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
					calls["memory_recall"]++
					return memory.RecallResult{}, errors.New("injected memory outage")
				},
				ObserveFunc: func(context.Context, memory.ObserveRequest) error {
					calls["memory_observe"]++
					return nil
				},
			}
			config := agent.Config{
				Model: countingTextModel(calls), Memory: port,
				MemoryScope:   memory.Scope{Namespace: "evaluation", Key: "reliability"},
				MemoryOptions: memory.Options{RecallFailureMode: memory.FailureModeFailRun},
			}
			return execute(ctx, config, nil, nil, calls)
		},
	}
}

func toolPanicScenario() scenario {
	return toolFailureScenario(
		"tool-panic", "tool implementation panics",
		func(context.Context, json.RawMessage) (tool.Result, error) { panic("injected tool panic") },
		failed(agent.ErrorToolRuntime, "tool_runtime_failed"),
	)
}

func toolCancellationScenario() scenario {
	return scenario{
		id: "tool-cancellation", category: "tool", fault: "tool cancels its Run context",
		expected:             failedWithStatus(agent.StatusCanceled, agent.ErrorCanceled, "tool_canceled"),
		requiredEvents:       []event.Type{event.ToolCallStarted, event.ToolCallCompleted, event.RuntimeError, event.RunCanceled},
		expectedMaxDelegates: map[string]int{"model": 1, "tool": 1},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			toolValue := evaluationTool(func(toolCtx context.Context, _ json.RawMessage) (tool.Result, error) {
				calls["tool"]++
				cancel()
				<-toolCtx.Done()
				return tool.Result{}, toolCtx.Err()
			})
			config, err := toolAgentConfig(calls, toolValue)
			if err != nil {
				return observation{delegateCalls: calls}, err
			}
			return execute(callCtx, config, nil, nil, calls)
		},
	}
}

func invalidToolResultScenario() scenario {
	return toolFailureScenario(
		"invalid-tool-result", "tool returns an empty Result without an error",
		func(context.Context, json.RawMessage) (tool.Result, error) { return tool.Result{}, nil },
		failed(agent.ErrorToolRuntime, "tool_runtime_failed"),
	)
}

func toolFailureScenario(id, fault string, executeTool func(context.Context, json.RawMessage) (tool.Result, error), expected Outcome) scenario {
	return scenario{
		id: id, category: "tool", fault: fault, expected: expected,
		requiredEvents:       []event.Type{event.ToolCallStarted, event.ToolCallCompleted, event.RuntimeError, event.RunFailed},
		expectedMaxDelegates: map[string]int{"model": 1, "tool": 1},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			toolValue := evaluationTool(func(toolCtx context.Context, arguments json.RawMessage) (tool.Result, error) {
				calls["tool"]++
				return executeTool(toolCtx, arguments)
			})
			config, err := toolAgentConfig(calls, toolValue)
			if err != nil {
				return observation{delegateCalls: calls}, err
			}
			return execute(ctx, config, nil, nil, calls)
		},
	}
}

func eventSinkFailureScenario() scenario {
	return scenario{
		id: "event-sink-failure", category: "observability", fault: "event Sink rejects model.call.started",
		expected:       failed(agent.ErrorEventSink, "event_sink_failed"),
		requiredEvents: []event.Type{event.ModelCallStarted, event.ModelCallCompleted, event.RuntimeError, event.RunFailed},
		expectedMaxDelegates: map[string]int{
			"model": 0, "event_sink": 2,
		},
		run: func(ctx context.Context) (observation, error) {
			calls := map[string]int{}
			sink := func(value event.Event) error {
				calls["event_sink"]++
				if value.Type == event.ModelCallStarted {
					return errors.New("injected event consumer failure")
				}
				return nil
			}
			return execute(ctx, agent.Config{Model: countingTextModel(calls)}, nil, sink, calls)
		},
	}
}

func execute(ctx context.Context, config agent.Config, previous *agent.State, sink event.Sink, calls map[string]int) (observation, error) {
	config.Name = "reliability-evaluation"
	config.ModelIdentity = evaluationIdentity
	config.IDs = agenttest.SequentialIDs()
	config.Now = agenttest.StepClock(time.Unix(1_700_000_000, 0).UTC(), time.Millisecond)
	if config.Limits == (agent.Limits{}) {
		config.Limits = agent.DefaultLimits()
	}
	value, err := agent.New(config)
	if err != nil {
		return observation{delegateCalls: calls}, err
	}
	var result agent.Result
	var runErr error
	if previous == nil {
		result, runErr = value.RunStream(ctx, userMessage("evaluate reliability"), sink)
	} else {
		result, runErr = value.Continue(ctx, *previous, userMessage("evaluate reliability"), sink)
	}
	return observation{result: result, err: runErr, delegateCalls: calls}, nil
}

func countingTextModel(calls map[string]int) model.Model {
	return agenttest.FuncModel(func(context.Context, model.Request, model.DeltaSink) (model.Response, error) {
		calls["model"]++
		return textResponse("completed"), nil
	})
}

func tokenPlanner(calls map[string]int, count func(model.TokenRequest) int64) (*contextmanager.TokenPlanner, error) {
	return contextmanager.NewTokenPlanner(contextmanager.TokenPlannerOptions{
		Counter: agenttest.FuncTokenCounter(func(_ context.Context, request model.TokenRequest) (int64, error) {
			calls["token_counter"]++
			return count(request), nil
		}),
		CountSource: contextmanager.CountSourceEstimated, ContextWindowTokens: 1000, SafetyMarginTokens: 50,
	})
}

func toolAgentConfig(calls map[string]int, value tool.Tool) (agent.Config, error) {
	kit, err := toolkit.New(value)
	if err != nil {
		return agent.Config{}, err
	}
	provider := agenttest.FuncModel(func(context.Context, model.Request, model.DeltaSink) (model.Response, error) {
		calls["model"]++
		return toolCallResponse("evaluation_call", "evaluation.tool"), nil
	})
	return agent.Config{Model: provider, Toolkit: kit}, nil
}

func evaluationTool(executeTool func(context.Context, json.RawMessage) (tool.Result, error)) tool.Tool {
	return agenttest.FuncTool{
		DefinitionValue: model.ToolDefinition{
			Name: "evaluation.tool", Description: "Deterministic reliability evaluation tool.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		},
		ExecuteFunc: executeTool,
	}
}

func userMessage(text string) message.Message {
	return message.Message{
		Role: message.RoleUser, Visibility: message.VisibilityPublic,
		Content: []message.Content{{Type: message.ContentText, Text: text}},
	}
}

func textResponse(text string) model.Response {
	return model.Response{
		Message:    message.Message{Content: []message.Content{{Type: message.ContentText, Text: text}}},
		StopReason: model.StopReasonComplete,
	}
}

func toolCallResponse(id, name string) model.Response {
	return model.Response{
		Message: message.Message{Content: []message.Content{{
			Type:     message.ContentToolCall,
			ToolCall: &message.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(`{}`)},
		}}},
		StopReason: model.StopReasonToolCall,
	}
}

func historicalState() agent.State {
	return agent.State{Messages: []message.Message{
		{ID: "history_user", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "old question"}}},
		{ID: "history_assistant", Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "old answer"}}},
	}}
}

func failed(class agent.ErrorClass, code string) Outcome {
	return failedWithStatus(agent.StatusFailed, class, code)
}

func failedWithStatus(status agent.Status, class agent.ErrorClass, code string) Outcome {
	return Outcome{Status: string(status), ErrorClass: string(class), ErrorCode: code}
}
