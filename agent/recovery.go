package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/approval"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

type recoveryKind string

const (
	recoveryModelResponse recoveryKind = "model_response"
	recoveryToolResult    recoveryKind = "tool_result"
)

type recoveryStart struct {
	kind                recoveryKind
	sourceTransitionID  string
	appliedTransitionID string
}

type preparedRecovery struct {
	kind  recoveryKind
	state State
}

// Recover validates and applies a durably saved completed Model or Tool
// Transition, then continues execution without requiring callers to mutate
// State. A TransitionHook is required so the recovered State is committed
// before the Runtime performs another Model or Tool effect.
func (a *Agent) Recover(ctx context.Context, completed Transition, sink event.Sink) (Result, error) {
	if ctx == nil {
		runtimeError := recoveryInputError("invalid_recovery_context", "Recovery requires a Context.", fmt.Errorf("context is nil"))
		return recoveryFailureResult(completed), runtimeError
	}
	if a.config.TransitionHook == nil {
		runtimeError := recoveryInputError("recovery_hook_required", "Recovery requires a durable Transition Hook.", nil)
		return recoveryFailureResult(completed), runtimeError
	}
	prepared, runtimeError := a.prepareRecovery(completed)
	if runtimeError != nil {
		return recoveryFailureResult(completed), runtimeError
	}

	transitions := &transitionEmitter{
		hook: a.config.TransitionHook, runID: a.config.IDs("recovery"), ids: a.config.IDs, now: a.config.Now,
	}
	applied, err := transitions.commit(ctx, TransitionRecoveryContinuationApplied, prepared.state, transitionFields{
		parentTransitionID: completed.ID,
		continuationID:     completed.ID,
		idempotencyKey:     stableTransitionKey("recovery-continuation", completed.ID),
		outcome:            string(prepared.kind),
	})
	if err != nil {
		runtimeError = transitionRunError("recovery_continuation_failed", err)
		return recoveryFailureResult(completed), runtimeError
	}

	return a.run(ctx, prepared.state, nil, nil, sink, &recoveryStart{
		kind: prepared.kind, sourceTransitionID: completed.ID, appliedTransitionID: applied.ID,
	})
}

func (a *Agent) prepareRecovery(completed Transition) (preparedRecovery, *RunError) {
	if err := completed.Validate(); err != nil {
		return preparedRecovery{}, recoveryInputError("invalid_recovery_continuation", "The recovery Transition is invalid.", err)
	}
	if completed.Checkpoint.Suspension != nil {
		return preparedRecovery{}, recoveryInputError("invalid_recovery_continuation", "Recovery cannot apply a result to a suspended State.", nil)
	}
	switch completed.Type {
	case TransitionModelCallCompleted:
		return a.prepareModelRecovery(completed)
	case TransitionToolEffectCompleted:
		return a.prepareToolRecovery(completed)
	default:
		return preparedRecovery{}, recoveryInputError("unsupported_recovery_transition", "Recovery accepts only completed Model or Tool Transitions.", nil)
	}
}

func (a *Agent) prepareModelRecovery(completed Transition) (preparedRecovery, *RunError) {
	if completed.Outcome != "completed" || completed.Private.ModelResponse == nil {
		return preparedRecovery{}, recoveryInputError("incomplete_model_recovery", "The Model Transition has no completed response to recover.", nil)
	}
	if err := completed.Checkpoint.ValidateComplete(); err != nil {
		return preparedRecovery{}, recoveryInputError("invalid_recovery_state", "The Model recovery checkpoint is not complete.", err)
	}
	response := normalizeRecoveredResponse(*completed.Private.ModelResponse, completed.ID)
	if err := response.Validate(); err != nil {
		return preparedRecovery{}, recoveryInputError("invalid_recovery_result", "The saved Model response is invalid.", err)
	}
	calls := toolCallsFrom(response.Message)
	if (len(calls) == 0 && response.StopReason != model.StopReasonComplete) ||
		(len(calls) > 0 && response.StopReason != model.StopReasonToolCall) {
		return preparedRecovery{}, recoveryInputError("invalid_recovery_result", "The saved Model response has an incompatible stop reason.", nil)
	}
	next := completed.Checkpoint.Clone()
	next.Usage = next.Usage.Add(response.Usage)
	next.Messages = append(next.Messages, message.Clone(response.Message))
	if err := next.Validate(); err != nil {
		return preparedRecovery{}, recoveryInputError("invalid_recovery_state", "The saved Model response conflicts with its checkpoint.", err)
	}
	if limitErr := a.checkUsage(next.Usage); limitErr != nil {
		return preparedRecovery{}, limitErr
	}
	return preparedRecovery{kind: recoveryModelResponse, state: next}, nil
}

func (a *Agent) prepareToolRecovery(completed Transition) (preparedRecovery, *RunError) {
	if (completed.Outcome != "succeeded" && completed.Outcome != "tool_error") || completed.Private.ToolResult == nil {
		return preparedRecovery{}, recoveryInputError("incomplete_tool_recovery", "The Tool Transition has no completed result to recover.", nil)
	}
	if (completed.Outcome == "succeeded" && (completed.Private.ToolResultIsError || completed.Private.ToolResultRetryable)) ||
		(completed.Outcome == "tool_error" && (!completed.Private.ToolResultStateReady || !completed.Private.ToolResultIsError)) {
		return preparedRecovery{}, recoveryInputError("invalid_recovery_result", "The saved Tool Result flags do not match its outcome.", nil)
	}
	state := completed.Checkpoint.Clone()
	if err := state.Validate(); err != nil {
		return preparedRecovery{}, recoveryInputError("invalid_recovery_state", "The Tool recovery checkpoint is invalid.", err)
	}
	pending := state.PendingToolCalls()
	if len(pending) == 0 || pending[0].ID != completed.CallID {
		return preparedRecovery{}, recoveryInputError("recovery_call_mismatch", "The Tool recovery does not match the next pending call.", nil)
	}
	call := pending[0]
	if !bytes.Equal(completed.Private.ToolArguments, call.Arguments) {
		return preparedRecovery{}, recoveryInputError("recovery_arguments_mismatch", "The saved Tool arguments do not match the checkpoint.", nil)
	}
	view, err := a.toolView()
	if err != nil {
		return preparedRecovery{}, &RunError{Class: ErrorInternal, Code: "toolkit_snapshot_failed", SafeMessage: "The Agent could not create its Tool snapshot.", Cause: err}
	}
	if _, exists := view.DefinitionDigest(call.Name); !exists {
		return preparedRecovery{}, recoveryInputError("recovery_toolkit_mismatch", "The recovered Tool is not available in the current Toolkit.", nil)
	}
	bindings, err := a.transitionToolBindings(state, view, []message.ToolCall{call})
	if err != nil || len(completed.Tools) != 1 || completed.Tools[0] != bindings[0] {
		return preparedRecovery{}, recoveryInputError("recovery_toolkit_mismatch", "The recovered Tool binding does not match the current Agent.", err)
	}
	if a.config.ArtifactOffloader != nil && !completed.Private.ToolResultStateReady {
		return preparedRecovery{}, recoveryInputError(
			"legacy_tool_result_projection_required",
			"The saved Tool Result predates recoverable Artifact projection and cannot be applied automatically.", nil,
		)
	}
	result := cloneToolResult(*completed.Private.ToolResult)
	if err := appendRecoveredToolResult(
		&state, call, result, completed.Private.ToolResultIsError, completed.Private.ToolResultRetryable, completed.ID,
	); err != nil {
		return preparedRecovery{}, recoveryInputError("invalid_recovery_result", "The saved Tool Result conflicts with its checkpoint.", err)
	}
	return preparedRecovery{kind: recoveryToolResult, state: state}, nil
}

func normalizeRecoveredResponse(response model.Response, sourceTransitionID string) model.Response {
	response.Message = message.Clone(response.Message)
	if response.Message.ID == "" {
		response.Message.ID = recoveryMessageID(sourceTransitionID)
	}
	if response.Message.Role == "" {
		response.Message.Role = message.RoleAssistant
	}
	if response.Message.Visibility == "" {
		response.Message.Visibility = message.VisibilityPublic
	}
	return response
}

func appendRecoveredToolResult(state *State, call message.ToolCall, result tool.Result, isError, retryable bool, sourceTransitionID string) error {
	next := state.Clone()
	next.Messages = append(next.Messages, message.Message{
		ID: recoveryMessageID(sourceTransitionID), Role: message.RoleTool, Visibility: message.VisibilityInternal,
		Content: []message.Content{{Type: message.ContentToolResult, ToolResult: &message.ToolResult{
			CallID: call.ID, Name: call.Name, Content: message.CloneContent(result.Content), State: append(json.RawMessage(nil), result.State...),
			IsError: isError, Retryable: retryable,
		}}},
	})
	if err := next.Validate(); err != nil {
		return err
	}
	*state = next
	return nil
}

func recoveryMessageID(sourceTransitionID string) string {
	return "msg_recovery_" + strings.TrimPrefix(stableTransitionKey("recovery-message", sourceTransitionID), "sha256:")
}

func recoveryTurn(state State) (int, message.Message, error) {
	if err := state.Validate(); err != nil {
		return 0, message.Message{}, err
	}
	for index := len(state.Messages) - 1; index >= 0; index-- {
		if state.Messages[index].Role == message.RoleUser {
			return index, message.Clone(state.Messages[index]), nil
		}
	}
	return 0, message.Message{}, fmt.Errorf("State has no User message")
}

func (a *Agent) recoveryApprovalPlan(view toolkit.View, calls []message.ToolCall) []approval.PendingCall {
	plan := make([]approval.PendingCall, 0, len(calls))
	for _, call := range calls {
		toolDigest, exists := view.DefinitionDigest(call.Name)
		if !exists {
			toolDigest = approval.ArgumentsDigest("unsupported", json.RawMessage(`null`))
		}
		plan = append(plan, approval.PendingCall{
			CallID: call.ID, Tool: call.Name,
			ArgumentsDigest: approval.ArgumentsDigest(call.Name, call.Arguments), ToolDigest: toolDigest,
			Behavior: approval.BehaviorRequire, ApprovalID: a.config.IDs("approval"),
			Reason: "Recovery requires approval before a remaining Tool call can execute.",
		})
	}
	return plan
}

func recoveryInputError(code, safeMessage string, cause error) *RunError {
	return &RunError{Class: ErrorInvalidInput, Code: code, SafeMessage: safeMessage, Cause: cause}
}

func recoveryFailureResult(completed Transition) Result {
	return Result{Status: StatusFailed, State: completed.Checkpoint.Clone()}
}
