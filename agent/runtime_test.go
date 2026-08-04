package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestSuccessfulToolLoop(t *testing.T) {
	t.Parallel()

	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "echo")},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				result := lastToolResult(request.Messages)
				if result == nil || result.IsError || result.Content[0].Text != "hello" {
					return fmt.Errorf("unexpected tool result: %#v", result)
				}
				return nil
			},
			Response: textResponse("done"),
		},
	)
	value := newAgent(t, script, agenttest.FuncTool{
		DefinitionValue: toolDefinition("echo"),
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.TextResult("hello"), nil
		},
	}, agent.DefaultLimits())
	trace := &agenttest.Trace{}

	result, err := value.RunStream(context.Background(), userMessage("say hello"), trace.Sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted || result.Final == nil || result.Final.Content[0].Text != "done" {
		t.Fatalf("result = %#v", result)
	}
	assertTerminalInvariants(t, result)
	if err := agenttest.ValidateTrace(trace.Events()); err != nil {
		t.Fatalf("streamed trace: %v", err)
	}
	if got := len(script.Calls()); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
}

func TestSafeToolErrorReturnsToModel(t *testing.T) {
	t.Parallel()

	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "lookup")},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				result := lastToolResult(request.Messages)
				if result == nil || !result.IsError || !result.Retryable || result.Content[0].Text != "Try a narrower query." {
					return fmt.Errorf("safe error was not projected to the model: %#v", result)
				}
				return nil
			},
			Response: textResponse("recovered"),
		},
	)
	value := newAgent(t, script, agenttest.FuncTool{
		DefinitionValue: toolDefinition("lookup"),
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{}, tool.NewError("too_broad", "Try a narrower query.", true, nil)
		},
	}, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("find it"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}
	assertTerminalInvariants(t, result)
}

func TestRuntimeToolErrorCannotBeDowngradedByNestedSafeError(t *testing.T) {
	t.Parallel()

	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "unsafe_wrapper")})
	value := newAgent(t, script, agenttest.FuncTool{
		DefinitionValue: toolDefinition("unsafe_wrapper"),
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{}, &tool.RuntimeError{
				Code:  "adapter_failed",
				Cause: tool.NewError("nested_safe", "This message must not reach the model.", false, nil),
			}
		},
	}, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("run it"))
	assertRunError(t, err, agent.ErrorToolRuntime, "tool_runtime_failed")
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	projected := lastToolResult(result.State.Messages)
	if projected == nil || !projected.IsError || projected.Content[0].Text == "This message must not reach the model." {
		t.Fatalf("runtime failure was downgraded: %#v", projected)
	}
	assertTerminalInvariants(t, result)
}

func TestAgentProjectsOnlyConfiguredToolGroups(t *testing.T) {
	t.Parallel()

	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, registration := range []toolkit.Registration{
		{
			Tool: agenttest.FuncTool{
				DefinitionValue: toolDefinition("lookup"),
				ExecuteFunc:     func(context.Context, json.RawMessage) (tool.Result, error) { return tool.TextResult("ok"), nil },
			},
			Namespace: "crm", Groups: []string{"read"},
		},
		{
			Tool: agenttest.FuncTool{
				DefinitionValue: toolDefinition("delete"),
				ExecuteFunc:     func(context.Context, json.RawMessage) (tool.Result, error) { return tool.TextResult("deleted"), nil },
			},
			Namespace: "crm", Groups: []string{"write"},
		},
	} {
		if err := kit.RegisterWith(registration); err != nil {
			t.Fatal(err)
		}
	}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			if len(request.Tools) != 1 || request.Tools[0].Name != "crm.lookup" {
				return fmt.Errorf("projected tools = %#v", request.Tools)
			}
			return nil
		},
		Response: textResponse("done"),
	})
	value, err := agent.New(agent.Config{
		Name: "group-test", Model: script,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		Toolkit:       kit, ToolGroups: []string{"read"},
		IDs: agenttest.SequentialIDs(),
		Now: agenttest.StepClock(time.Unix(1_700_000_000, 0), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := value.Run(context.Background(), userMessage("lookup"))
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalInvariants(t, result)
}

func TestModelFailure(t *testing.T) {
	t.Parallel()

	script := agenttest.NewScriptedModel(agenttest.ModelStep{Err: &model.ProviderError{
		Class: model.ErrorUnavailable, SafeDetail: "Provider unavailable.", Retryable: true,
	}})
	value := newAgent(t, script, nil, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("hello"))
	assertRunError(t, err, agent.ErrorModel, string(model.ErrorUnavailable))
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	assertTerminalInvariants(t, result)
}

func TestConflictingModelResponseDoesNotCorruptState(t *testing.T) {
	t.Parallel()

	response := model.Response{
		Message: message.Message{Content: []message.Content{
			{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "duplicate", Name: "echo", Arguments: json.RawMessage(`{}`)}},
			{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "duplicate", Name: "echo", Arguments: json.RawMessage(`{}`)}},
		}},
		StopReason: model.StopReasonToolCall,
	}
	value := newAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: response}), echoTool(), agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("duplicate"))
	assertRunError(t, err, agent.ErrorModel, "invalid_model_response")
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if got := countToolResults(result.State.Messages); got != 0 {
		t.Fatalf("tool results = %d, want 0 for rejected response", got)
	}
	assertTerminalInvariants(t, result)
}

func TestToolPanicTerminatesRun(t *testing.T) {
	t.Parallel()

	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "panic")})
	value := newAgent(t, script, agenttest.FuncTool{
		DefinitionValue: toolDefinition("panic"),
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			panic("boom")
		},
	}, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("run it"))
	assertRunError(t, err, agent.ErrorToolRuntime, "tool_runtime_failed")
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	resultMessage := lastToolResult(result.State.Messages)
	if resultMessage == nil || !resultMessage.IsError {
		t.Fatalf("terminal state does not contain a safe tool error")
	}
	assertTerminalInvariants(t, result)
}

func TestModelPanicTerminatesRun(t *testing.T) {
	t.Parallel()

	value := newAgent(t, agenttest.FuncModel(func(context.Context, model.Request, model.DeltaSink) (model.Response, error) {
		panic("model boom")
	}), nil, agent.DefaultLimits())

	result, err := value.Run(context.Background(), userMessage("run it"))
	assertRunError(t, err, agent.ErrorModel, "model_failed")
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	assertTerminalInvariants(t, result)
}

func TestCancellationPropagatesToModel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	value := newAgent(t, agenttest.FuncModel(func(callCtx context.Context, _ model.Request, _ model.DeltaSink) (model.Response, error) {
		cancel()
		<-callCtx.Done()
		return model.Response{}, callCtx.Err()
	}), nil, agent.DefaultLimits())

	result, err := value.Run(ctx, userMessage("wait"))
	assertRunError(t, err, agent.ErrorCanceled, "model_canceled")
	if result.Status != agent.StatusCanceled {
		t.Fatalf("status = %q", result.Status)
	}
	assertTerminalInvariants(t, result)
}

func TestSinkFailureAbortsWithBalancedInternalTrace(t *testing.T) {
	t.Parallel()

	value := newAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("unused")}), nil, agent.DefaultLimits())
	sinkErr := errors.New("consumer stopped")
	sink := func(value event.Event) error {
		if value.Type == event.ModelCallStarted {
			return sinkErr
		}
		return nil
	}

	result, err := value.RunStream(context.Background(), userMessage("hello"), sink)
	assertRunError(t, err, agent.ErrorEventSink, "event_sink_failed")
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	assertTerminalInvariants(t, result)
}

func TestSinkPanicAbortsWithBalancedInternalTrace(t *testing.T) {
	t.Parallel()

	value := newAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("unused")}), nil, agent.DefaultLimits())
	result, err := value.RunStream(context.Background(), userMessage("hello"), func(event.Event) error {
		panic("sink boom")
	})
	assertRunError(t, err, agent.ErrorEventSink, "run_start_failed")
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	assertTerminalInvariants(t, result)
}

func TestTerminalSinkFailurePreservesExecutionStatus(t *testing.T) {
	t.Parallel()

	value := newAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")}), nil, agent.DefaultLimits())
	sink := func(value event.Event) error {
		if value.Type == event.RunCompleted {
			return errors.New("terminal delivery failed")
		}
		return nil
	}

	result, err := value.RunStream(context.Background(), userMessage("hello"), sink)
	assertRunError(t, err, agent.ErrorEventSink, "terminal_event_failed")
	if result.Status != agent.StatusCompleted || result.Final == nil {
		t.Fatalf("execution result = %#v", result)
	}
	assertTerminalInvariants(t, result)
}

func TestExecutionLimitsClosePendingToolCalls(t *testing.T) {
	t.Parallel()

	t.Run("model calls", func(t *testing.T) {
		limits := agent.DefaultLimits()
		limits.MaxModelCalls = 1
		limits.MaxIterations = 2
		value := newAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "echo")}), echoTool(), limits)
		result, err := value.Run(context.Background(), userMessage("loop"))
		assertRunError(t, err, agent.ErrorLimit, "max_model_calls")
		assertTerminalInvariants(t, result)
	})

	t.Run("iterations", func(t *testing.T) {
		limits := agent.DefaultLimits()
		limits.MaxIterations = 1
		limits.MaxModelCalls = 2
		value := newAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "echo")}), echoTool(), limits)
		result, err := value.Run(context.Background(), userMessage("loop"))
		assertRunError(t, err, agent.ErrorLimit, "max_iterations")
		assertTerminalInvariants(t, result)
	})

	t.Run("tool calls", func(t *testing.T) {
		limits := agent.DefaultLimits()
		limits.MaxToolCalls = 1
		response := model.Response{
			Message: message.Message{Content: []message.Content{
				{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{}`)}},
				{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call_2", Name: "echo", Arguments: json.RawMessage(`{}`)}},
			}},
			StopReason: model.StopReasonToolCall,
		}
		value := newAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: response}), echoTool(), limits)
		result, err := value.Run(context.Background(), userMessage("twice"))
		assertRunError(t, err, agent.ErrorLimit, "max_tool_calls")
		assertTerminalInvariants(t, result)
		if got := countToolResults(result.State.Messages); got != 2 {
			t.Fatalf("tool results = %d, want 2", got)
		}
	})

	t.Run("token usage", func(t *testing.T) {
		limits := agent.DefaultLimits()
		limits.MaxOutputTokens = 1
		response := textResponse("too many tokens")
		response.Usage = model.Usage{OutputTokens: 2, Source: model.UsageSourceProvider}
		value := newAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: response}), nil, limits)
		result, err := value.Run(context.Background(), userMessage("answer"))
		assertRunError(t, err, agent.ErrorLimit, "max_output_tokens")
		assertTerminalInvariants(t, result)
	})
}

func newAgent(t *testing.T, modelValue model.Model, toolValue tool.Tool, limits agent.Limits) *agent.Agent {
	t.Helper()
	var tools []tool.Tool
	if toolValue != nil {
		tools = append(tools, toolValue)
	}
	kit, err := toolkit.New(tools...)
	if err != nil {
		t.Fatal(err)
	}
	value, err := agent.New(agent.Config{
		Name: "test", Model: modelValue, ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		Toolkit: kit, Limits: limits, IDs: agenttest.SequentialIDs(),
		Now: agenttest.StepClock(time.Unix(1_700_000_000, 0), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func userMessage(text string) message.Message {
	return message.Message{Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: text}}}
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
			Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(`{}`)},
		}}},
		StopReason: model.StopReasonToolCall,
	}
}

func toolDefinition(name string) model.ToolDefinition {
	return model.ToolDefinition{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func echoTool() tool.Tool {
	return agenttest.FuncTool{
		DefinitionValue: toolDefinition("echo"),
		ExecuteFunc:     func(context.Context, json.RawMessage) (tool.Result, error) { return tool.TextResult("ok"), nil },
	}
}

func lastToolResult(messages []message.Message) *message.ToolResult {
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		for contentIndex := len(messages[messageIndex].Content) - 1; contentIndex >= 0; contentIndex-- {
			if result := messages[messageIndex].Content[contentIndex].ToolResult; result != nil {
				return result
			}
		}
	}
	return nil
}

func countToolResults(messages []message.Message) int {
	count := 0
	for _, value := range messages {
		for _, content := range value.Content {
			if content.ToolResult != nil {
				count++
			}
		}
	}
	return count
}

func assertRunError(t *testing.T, err error, class agent.ErrorClass, code string) {
	t.Helper()
	var runError *agent.RunError
	if !errors.As(err, &runError) {
		t.Fatalf("error = %v, want RunError", err)
	}
	if runError.Class != class || runError.Code != code {
		t.Fatalf("RunError = %s/%s, want %s/%s", runError.Class, runError.Code, class, code)
	}
}

func assertTerminalInvariants(t *testing.T, result agent.Result) {
	t.Helper()
	if err := result.State.ValidateComplete(); err != nil {
		t.Fatalf("terminal state: %v", err)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatalf("result trace: %v\ntypes: %v", err, agenttest.EventTypes(result.Events))
	}
}
