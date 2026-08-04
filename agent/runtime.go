package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

type Status string

const (
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

type Result struct {
	Status     Status           `json:"status"`
	State      State            `json:"state"`
	Final      *message.Message `json:"final,omitempty"`
	Iterations int              `json:"iterations"`
	Events     []event.Event    `json:"events"`
}

func (a *Agent) Run(ctx context.Context, input message.Message) (Result, error) {
	return a.run(ctx, State{}, input, nil)
}

func (a *Agent) RunStream(ctx context.Context, input message.Message, sink event.Sink) (Result, error) {
	return a.run(ctx, State{}, input, sink)
}

func (a *Agent) Continue(ctx context.Context, previous State, input message.Message, sink event.Sink) (Result, error) {
	return a.run(ctx, previous, input, sink)
}

func (a *Agent) run(parent context.Context, previous State, input message.Message, sink event.Sink) (result Result, runErr error) {
	ctx, cancel := context.WithTimeout(parent, a.config.Limits.MaxDuration)
	defer cancel()

	runID := a.config.IDs("run")
	emit := &emitter{runID: runID, ids: a.config.IDs, now: a.config.Now, sink: sink}
	state := previous.Clone()
	view, viewErr := a.toolView()
	modelCalls := 0
	toolCalls := 0
	iterations := 0
	startedTools := make(map[string]string)

	_, _ = emit.emit(event.RunStarted, "", a.runStartedPayload())
	finish := func(status Status, reason string, err *RunError, final *message.Message) (Result, error) {
		if len(state.PendingToolCalls()) > 0 {
			a.closePendingTools(&state, emit, startedTools, "interrupted", "Tool execution was interrupted before a result was produced.")
		}
		if stateErr := state.ValidateComplete(); stateErr != nil {
			status = StatusFailed
			reason = "invalid_final_state"
			err = &RunError{Class: ErrorInternal, Code: "invalid_final_state", SafeMessage: "The Agent produced an invalid final state.", Cause: stateErr}
			final = nil
		}
		if emit.sinkErr != nil && (err == nil || err.Class != ErrorEventSink) {
			status = StatusFailed
			reason = "event_sink_failed"
			err = &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}
			final = nil
		}
		var payloadError *event.ErrorPayload
		if err != nil {
			payloadError = eventError(err)
			_, _ = emit.emit(event.RuntimeError, "", payloadError)
		}
		// A sink can fail while consuming RuntimeError. This is still before the
		// terminal event, so delivery failure becomes the terminal failure.
		if emit.sinkErr != nil && (err == nil || err.Class != ErrorEventSink) {
			status = StatusFailed
			reason = "event_sink_failed"
			err = &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}
			payloadError = eventError(err)
			final = nil
		}
		terminalType := event.RunCompleted
		if status == StatusFailed {
			terminalType = event.RunFailed
		} else if status == StatusCanceled {
			terminalType = event.RunCanceled
		}
		_, terminalEmitErr := emit.emit(terminalType, "", event.RunTerminalPayload{
			Status: string(status), Reason: reason, Iterations: iterations, Usage: state.Usage, Error: payloadError,
		})
		if terminalEmitErr != nil {
			err = &RunError{
				Class: ErrorEventSink, Code: "terminal_event_failed", SafeMessage: "The terminal event could not be delivered.",
				Cause: errors.Join(err, terminalEmitErr),
			}
		}
		result := Result{
			Status: status, State: state.Clone(), Final: cloneMessagePointer(final), Iterations: iterations, Events: cloneEvents(emit.events),
		}
		if err == nil {
			return result, nil
		}
		return result, err
	}

	if emit.sinkErr != nil {
		return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "run_start_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}, nil)
	}
	if viewErr != nil {
		return finish(StatusFailed, "toolkit_snapshot_failed", &RunError{
			Class: ErrorInternal, Code: "toolkit_snapshot_failed", SafeMessage: "The Agent could not create its Tool snapshot.", Cause: viewErr,
		}, nil)
	}
	if err := state.ValidateComplete(); err != nil {
		return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInvalidInput, Code: "invalid_state", SafeMessage: "The previous Agent state is invalid.", Cause: err}, nil)
	}
	if err := a.checkUsage(state.Usage); err != nil {
		return finish(StatusFailed, "usage_limit", err, nil)
	}
	input = a.normalizeInput(input)
	if err := input.Validate(); err != nil || input.Role != message.RoleUser {
		if err == nil {
			err = fmt.Errorf("input role must be user")
		}
		return finish(StatusFailed, "invalid_input", &RunError{Class: ErrorInvalidInput, Code: "invalid_message", SafeMessage: "The input message is invalid.", Cause: err}, nil)
	}
	nextState := state.Clone()
	if prompt := a.systemPrompt(); len(nextState.Messages) == 0 && prompt != "" {
		nextState.Messages = append(nextState.Messages, message.Message{
			ID: a.config.IDs("msg"), Role: message.RoleSystem, Visibility: message.VisibilityInternal,
			Content: []message.Content{{Type: message.ContentText, Text: prompt}},
		})
	}
	turnStart := len(nextState.Messages)
	nextState.Messages = append(nextState.Messages, message.Clone(input))
	if err := nextState.Validate(); err != nil {
		return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInvalidInput, Code: "invalid_state", SafeMessage: "The Agent input conflicts with the previous state.", Cause: err}, nil)
	}
	state = nextState
	recall := a.recallMemory(ctx, runID, input, emit)
	if recall.err != nil {
		if recall.canceled {
			return finish(StatusCanceled, recall.reason, recall.err, nil)
		}
		return finish(StatusFailed, recall.reason, recall.err, nil)
	}

	for iterations < a.config.Limits.MaxIterations {
		if err := ctx.Err(); err != nil {
			return finish(StatusCanceled, "context_canceled", &RunError{Class: ErrorCanceled, Code: "context_canceled", SafeMessage: "The Agent run was canceled.", Cause: err}, nil)
		}
		if emit.sinkErr != nil {
			return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}, nil)
		}
		iterations++
		modelMessages := projectRecalledMemory(state.Messages, recall.projection)
		generation := a.generateModel(ctx, runID, modelMessages, view.Definitions(), emit, &modelCalls)
		if generation.err != nil {
			if generation.canceled {
				return finish(StatusCanceled, generation.reason, generation.err, nil)
			}
			return finish(StatusFailed, generation.reason, generation.err, nil)
		}
		response := generation.response
		response = a.normalizeResponse(response)
		if err := response.Validate(); err != nil {
			runtimeError := &RunError{Class: ErrorModel, Code: "invalid_model_response", SafeMessage: "The model returned an invalid response.", Cause: err}
			_, _ = emit.emit(event.ModelCallCompleted, generation.startedID, event.ModelCallPayload{
				CallID: generation.callID, Model: generation.target, Attempt: generation.attempt, Status: "failed", Error: eventError(runtimeError),
			})
			return finish(StatusFailed, "invalid_model_response", runtimeError, nil)
		}
		nextState := state.Clone()
		nextState.Usage = nextState.Usage.Add(response.Usage)
		nextState.Messages = append(nextState.Messages, message.Clone(response.Message))
		if err := nextState.Validate(); err != nil {
			// Usage describes a completed provider call even when its response
			// cannot be committed to the conversation state.
			state.Usage = nextState.Usage
			runtimeError := &RunError{Class: ErrorModel, Code: "invalid_model_response", SafeMessage: "The model response conflicts with the Agent state.", Cause: err}
			_, _ = emit.emit(event.ModelCallCompleted, generation.startedID, event.ModelCallPayload{
				CallID: generation.callID, Model: generation.target, Attempt: generation.attempt, Status: "failed", Error: eventError(runtimeError),
			})
			return finish(StatusFailed, "invalid_model_response", runtimeError, nil)
		}
		state = nextState
		if err := a.checkUsage(state.Usage); err != nil {
			_, _ = emit.emit(event.ModelCallCompleted, generation.startedID, event.ModelCallPayload{
				CallID: generation.callID, Model: generation.target, Attempt: generation.attempt, Status: "completed",
				StopReason: string(response.StopReason), Usage: response.Usage,
			})
			return finish(StatusFailed, "usage_limit", err, nil)
		}
		_, completedEmitErr := emit.emit(event.ModelCallCompleted, generation.startedID, event.ModelCallPayload{
			CallID: generation.callID, Model: generation.target, Attempt: generation.attempt, Status: "completed",
			StopReason: string(response.StopReason), Usage: response.Usage,
		})
		if completedEmitErr != nil {
			return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: completedEmitErr}, nil)
		}
		calls := toolCallsFrom(response.Message)
		if len(calls) == 0 {
			if response.StopReason != model.StopReasonComplete {
				return finish(StatusFailed, "unexpected_stop", &RunError{Class: ErrorModel, Code: "unexpected_stop_reason", SafeMessage: "The model stopped without completing or requesting a tool."}, nil)
			}
			if err := state.ValidateComplete(); err != nil {
				return finish(StatusFailed, "invalid_final_state", &RunError{Class: ErrorInternal, Code: "invalid_final_state", SafeMessage: "The Agent produced an invalid final state.", Cause: err}, nil)
			}
			final := message.Clone(response.Message)
			observation := a.observeMemory(ctx, runID, state.Messages[turnStart:], emit)
			if observation.err != nil {
				if observation.canceled {
					return finish(StatusCanceled, observation.reason, observation.err, nil)
				}
				return finish(StatusFailed, observation.reason, observation.err, nil)
			}
			return finish(StatusCompleted, "completed", nil, &final)
		}
		if response.StopReason != model.StopReasonToolCall {
			return finish(StatusFailed, "invalid_tool_stop", &RunError{Class: ErrorModel, Code: "tool_call_stop_mismatch", SafeMessage: "The model returned tool calls with an incompatible stop reason."}, nil)
		}
		if toolCalls+len(calls) > a.config.Limits.MaxToolCalls {
			return finish(StatusFailed, "tool_call_limit", &RunError{Class: ErrorLimit, Code: "max_tool_calls", SafeMessage: "The Agent reached its tool-call limit."}, nil)
		}

		for _, call := range calls {
			if err := ctx.Err(); err != nil {
				return finish(StatusCanceled, "context_canceled", &RunError{Class: ErrorCanceled, Code: "tool_canceled", SafeMessage: "Tool execution was canceled.", Cause: err}, nil)
			}
			toolCalls++
			toolStarted, err := emit.emit(event.ToolCallStarted, "", event.ToolCallPayload{CallID: call.ID, Name: call.Name})
			startedTools[call.ID] = toolStarted.ID
			if err != nil {
				return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: err}, nil)
			}
			toolResult, toolErr := view.Call(ctx, call)
			if ctx.Err() != nil {
				if err := a.appendToolError(&state, call, "canceled", "Tool execution was canceled.", false); err != nil {
					return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInternal, Code: "invalid_state", SafeMessage: "The Agent could not record a tool result.", Cause: err}, nil)
				}
				_, _ = emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{CallID: call.ID, Name: call.Name, Status: "canceled", IsError: true})
				delete(startedTools, call.ID)
				return finish(StatusCanceled, "context_canceled", &RunError{Class: ErrorCanceled, Code: "tool_canceled", SafeMessage: "Tool execution was canceled.", Cause: ctx.Err()}, nil)
			}
			if toolErr != nil {
				var runtimeFailure *tool.RuntimeError
				var safe *tool.Error
				if !errors.As(toolErr, &runtimeFailure) && errors.As(toolErr, &safe) {
					if err := a.appendToolError(&state, call, safe.Code, safe.SafeMessage, safe.Retryable); err != nil {
						return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInternal, Code: "invalid_state", SafeMessage: "The Agent could not record a tool result.", Cause: err}, nil)
					}
					_, _ = emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{
						CallID: call.ID, Name: call.Name, Status: "completed", IsError: true, Retryable: safe.Retryable,
						Error: &event.ErrorPayload{Class: "tool", Code: safe.Code, Message: safe.SafeMessage, Retryable: safe.Retryable},
					})
					delete(startedTools, call.ID)
					if emit.sinkErr != nil {
						return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}, nil)
					}
					continue
				}
				if err := a.appendToolError(&state, call, "runtime_failure", "Tool execution failed because of a runtime error.", false); err != nil {
					return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInternal, Code: "invalid_state", SafeMessage: "The Agent could not record a tool result.", Cause: err}, nil)
				}
				runtimeError := &RunError{Class: ErrorToolRuntime, Code: "tool_runtime_failed", SafeMessage: "A tool runtime failed.", Cause: toolErr}
				_, _ = emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{
					CallID: call.ID, Name: call.Name, Status: "failed", IsError: true, Error: eventError(runtimeError),
				})
				delete(startedTools, call.ID)
				return finish(StatusFailed, "tool_runtime_failed", runtimeError, nil)
			}
			projection := a.projectToolResult(ctx, runID, call, toolResult, toolStarted.ID, emit)
			if projection.err != nil {
				if projection.err.Class == ErrorEventSink {
					return finish(StatusFailed, "event_sink_failed", projection.err, nil)
				}
				code := "artifact_unavailable"
				messageText := "The Tool Result could not be stored safely."
				status := "failed"
				if projection.canceled {
					code = "canceled"
					messageText = "Tool Result offload was canceled."
					status = "canceled"
				}
				if err := a.appendToolError(&state, call, code, messageText, false); err != nil {
					return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInternal, Code: "invalid_state", SafeMessage: "The Agent could not record an Artifact failure.", Cause: err}, nil)
				}
				_, _ = emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{
					CallID: call.ID, Name: call.Name, Status: status, IsError: true, Error: eventError(projection.err),
				})
				delete(startedTools, call.ID)
				if projection.canceled {
					return finish(StatusCanceled, projection.reason, projection.err, nil)
				}
				return finish(StatusFailed, projection.reason, projection.err, nil)
			}
			if err := a.appendToolResult(&state, call, projection.result); err != nil {
				return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInternal, Code: "invalid_state", SafeMessage: "The Agent could not record a tool result.", Cause: err}, nil)
			}
			_, completedEmitErr := emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{CallID: call.ID, Name: call.Name, Status: "completed"})
			delete(startedTools, call.ID)
			if completedEmitErr != nil {
				return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: completedEmitErr}, nil)
			}
		}
	}

	return finish(StatusFailed, "iteration_limit", &RunError{Class: ErrorLimit, Code: "max_iterations", SafeMessage: "The Agent reached its iteration limit."}, nil)
}

func (a *Agent) toolView() (toolkit.View, error) {
	groups := effectiveToolGroups(a.config.ToolGroups, a.config.Skills)
	if len(groups) == 0 && (a.config.Skills == nil || a.config.Skills.Empty()) {
		return a.config.Toolkit.Snapshot(), nil
	}
	if a.config.Toolkit == nil {
		return a.config.Toolkit.Snapshot(), nil
	}
	return a.config.Toolkit.SnapshotGroups(groups...)
}

func (a *Agent) systemPrompt() string {
	active := a.config.Skills.SystemInstructions()
	if active == "" {
		return a.config.SystemPrompt
	}
	if a.config.SystemPrompt == "" {
		return active
	}
	return a.config.SystemPrompt + "\n\n" + active
}

func (a *Agent) runStartedPayload() event.RunStartedPayload {
	payload := event.RunStartedPayload{Agent: a.config.Name}
	payload.ToolGroups = effectiveToolGroups(a.config.ToolGroups, a.config.Skills)
	for _, active := range a.config.Skills.Skills() {
		payload.Skills = append(payload.Skills, event.ActivatedSkillPayload{
			Name: active.Name, Version: active.Version, Digest: active.Digest,
		})
	}
	return payload
}

func (a *Agent) normalizeInput(input message.Message) message.Message {
	input = message.Clone(input)
	if input.ID == "" {
		input.ID = a.config.IDs("msg")
	}
	if input.Role == "" {
		input.Role = message.RoleUser
	}
	if input.Visibility == "" {
		input.Visibility = message.VisibilityPublic
	}
	return input
}

func (a *Agent) normalizeResponse(response model.Response) model.Response {
	response.Message = message.Clone(response.Message)
	if response.Message.ID == "" {
		response.Message.ID = a.config.IDs("msg")
	}
	if response.Message.Role == "" {
		response.Message.Role = message.RoleAssistant
	}
	if response.Message.Visibility == "" {
		response.Message.Visibility = message.VisibilityPublic
	}
	return response
}

func (a *Agent) checkUsage(usage model.Usage) *RunError {
	limits := a.config.Limits
	switch {
	case usage.InputTokens > limits.MaxInputTokens:
		return &RunError{Class: ErrorLimit, Code: "max_input_tokens", SafeMessage: "The Agent reached its input-token limit."}
	case usage.OutputTokens > limits.MaxOutputTokens:
		return &RunError{Class: ErrorLimit, Code: "max_output_tokens", SafeMessage: "The Agent reached its output-token limit."}
	case usage.ReasoningTokens > limits.MaxReasoningTokens:
		return &RunError{Class: ErrorLimit, Code: "max_reasoning_tokens", SafeMessage: "The Agent reached its reasoning-token limit."}
	default:
		return nil
	}
}

func (a *Agent) appendToolResult(state *State, call message.ToolCall, result tool.Result) error {
	nextState := state.Clone()
	nextState.Messages = append(nextState.Messages, message.Message{
		ID: a.config.IDs("msg"), Role: message.RoleTool, Visibility: message.VisibilityInternal,
		Content: []message.Content{{Type: message.ContentToolResult, ToolResult: &message.ToolResult{
			CallID: call.ID, Name: call.Name, Content: message.CloneContent(result.Content), State: append(json.RawMessage(nil), result.State...),
		}}},
	})
	if err := nextState.Validate(); err != nil {
		return err
	}
	*state = nextState
	return nil
}

func (a *Agent) appendToolError(state *State, call message.ToolCall, code, safeMessage string, retryable bool) error {
	metadata, _ := json.Marshal(map[string]string{"code": code})
	if err := a.appendToolResult(state, call, tool.Result{
		Content: []message.Content{{Type: message.ContentText, Text: safeMessage}}, State: metadata,
	}); err != nil {
		return err
	}
	last := &state.Messages[len(state.Messages)-1]
	last.Content[0].ToolResult.IsError = true
	last.Content[0].ToolResult.Retryable = retryable
	return nil
}

func (a *Agent) closePendingTools(state *State, emit *emitter, started map[string]string, code, safeMessage string) {
	for _, call := range state.PendingToolCalls() {
		parentID := started[call.ID]
		if parentID == "" {
			startedEvent, _ := emit.emit(event.ToolCallStarted, "", event.ToolCallPayload{CallID: call.ID, Name: call.Name})
			parentID = startedEvent.ID
		}
		if err := a.appendToolError(state, call, code, safeMessage, false); err != nil {
			continue
		}
		_, _ = emit.emit(event.ToolCallCompleted, parentID, event.ToolCallPayload{
			CallID: call.ID, Name: call.Name, Status: "interrupted", IsError: true,
			Error: &event.ErrorPayload{Class: string(ErrorInternal), Code: code, Message: safeMessage},
		})
		delete(started, call.ID)
	}
}

func toolCallsFrom(value message.Message) []message.ToolCall {
	var calls []message.ToolCall
	for _, content := range value.Content {
		if content.ToolCall != nil {
			call := *content.ToolCall
			call.Arguments = append(json.RawMessage(nil), content.ToolCall.Arguments...)
			calls = append(calls, call)
		}
	}
	return calls
}

func eventError(err *RunError) *event.ErrorPayload {
	if err == nil {
		return nil
	}
	return &event.ErrorPayload{Class: string(err.Class), Code: err.Code, Message: err.SafeMessage, Retryable: err.Retryable}
}

func modelRunError(err error) *RunError {
	var provider *model.ProviderError
	if errors.As(err, &provider) {
		return &RunError{
			Class: ErrorModel, Code: string(provider.Class), SafeMessage: provider.Error(), Retryable: provider.Retryable, Cause: err,
		}
	}
	return &RunError{Class: ErrorModel, Code: "model_failed", SafeMessage: "The model call failed.", Cause: err}
}

func cloneMessagePointer(value *message.Message) *message.Message {
	if value == nil {
		return nil
	}
	cloned := message.Clone(*value)
	return &cloned
}
