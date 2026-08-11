package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/uyshero/tiggy-agent-runtime/approval"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func (e *transitionEmitter) enabled() bool {
	return e != nil && e.hook != nil
}

func (a *Agent) commitStateTransition(
	ctx context.Context,
	transitions *transitionEmitter,
	state *State,
	next State,
	cause string,
	parentTransitionID string,
) *RunError {
	if err := next.Validate(); err != nil {
		return &RunError{Class: ErrorInternal, Code: "invalid_state", SafeMessage: "The Agent produced an invalid state transition.", Cause: err}
	}
	if transitions.enabled() {
		if _, err := transitions.commit(ctx, TransitionStateCommitted, next, transitionFields{
			parentTransitionID: parentTransitionID,
			cause:              cause,
		}); err != nil {
			return transitionRunError("state_transition_failed", err)
		}
	}
	*state = next
	return nil
}

func (a *Agent) consumeContinuation(
	ctx context.Context,
	transitions *transitionEmitter,
	state *State,
	continuationID string,
) *RunError {
	next := state.Clone()
	next.Suspension = nil
	if err := next.Validate(); err != nil {
		return &RunError{Class: ErrorInternal, Code: "invalid_state", SafeMessage: "The Agent could not consume its approval continuation.", Cause: err}
	}
	if transitions.enabled() {
		if _, err := transitions.commit(ctx, TransitionResumeContinuationConsumed, next, transitionFields{
			continuationID: continuationID,
			idempotencyKey: stableTransitionKey("resume-continuation", continuationID),
		}); err != nil {
			return transitionRunError("resume_continuation_failed", err)
		}
	}
	*state = next
	return nil
}

func (a *Agent) freezeToolBatch(
	ctx context.Context,
	transitions *transitionEmitter,
	state State,
	view toolkit.View,
	calls []message.ToolCall,
) ([]TransitionToolBinding, string, *RunError) {
	bindings, err := a.transitionToolBindings(state, view, calls)
	if err != nil {
		return nil, "", &RunError{Class: ErrorInternal, Code: "invalid_tool_transition", SafeMessage: "The Agent could not bind its Tool transition metadata.", Cause: err}
	}
	batchDigest, err := digestTransitionBindings(bindings)
	if err != nil {
		return nil, "", &RunError{Class: ErrorInternal, Code: "invalid_tool_transition", SafeMessage: "The Agent could not digest its Tool batch.", Cause: err}
	}
	if transitions.enabled() {
		if _, err := transitions.commit(ctx, TransitionToolBatchFrozen, state, transitionFields{
			batchDigest: batchDigest,
			tools:       bindings,
		}); err != nil {
			return nil, "", transitionRunError("tool_batch_freeze_failed", err)
		}
	}
	return bindings, batchDigest, nil
}

func (a *Agent) transitionToolBindings(state State, view toolkit.View, calls []message.ToolCall) ([]TransitionToolBinding, error) {
	bindings := make([]TransitionToolBinding, len(calls))
	for index, call := range calls {
		toolDigest, exists := view.DefinitionDigest(call.Name)
		if !exists {
			toolDigest = approval.ArgumentsDigest("unsupported", json.RawMessage(`null`))
		}
		argumentsDigest := approval.ArgumentsDigest(call.Name, call.Arguments)
		messageID, exists := toolCallMessageID(state, call.ID)
		if !exists {
			return nil, fmt.Errorf("Tool call %q has no source Assistant message", call.ID)
		}
		effectClass := a.toolEffectClass(call.Name)
		bindings[index] = TransitionToolBinding{
			CallID:          call.ID,
			ToolDigest:      toolDigest,
			ArgumentsDigest: argumentsDigest,
			EffectClass:     effectClass,
			IdempotencyKey: stableTransitionKey(
				"tool-effect", messageID, call.ID, toolDigest, argumentsDigest, string(effectClass),
			),
		}
	}
	return bindings, nil
}

func bindApprovalPlanTransitions(plan []approval.PendingCall, bindings []TransitionToolBinding) error {
	if len(plan) != len(bindings) {
		return fmt.Errorf("approval plan and transition binding counts differ")
	}
	for index := range plan {
		if plan[index].CallID != bindings[index].CallID {
			return fmt.Errorf("approval plan call %d does not match transition binding", index)
		}
		plan[index].EffectClass = string(bindings[index].EffectClass)
		plan[index].IdempotencyKey = bindings[index].IdempotencyKey
	}
	return nil
}

func validateApprovalTransitionBindings(plan []approval.PendingCall, bindings []TransitionToolBinding) error {
	if len(plan) != len(bindings) {
		return fmt.Errorf("approval plan and transition binding counts differ")
	}
	for index := range plan {
		// Empty fields are accepted only for v0.1.2 continuations created before
		// durable transitions existed.
		if plan[index].EffectClass == "" && plan[index].IdempotencyKey == "" {
			continue
		}
		if plan[index].CallID != bindings[index].CallID ||
			plan[index].EffectClass != string(bindings[index].EffectClass) ||
			plan[index].IdempotencyKey != bindings[index].IdempotencyKey {
			return fmt.Errorf("approval call %d changed Tool effect binding", index)
		}
	}
	return nil
}

func toolCallMessageID(state State, callID string) (string, bool) {
	for _, value := range state.Messages {
		for _, content := range value.Content {
			if content.ToolCall != nil && content.ToolCall.ID == callID {
				return value.ID, true
			}
		}
	}
	return "", false
}

func commitToolEffectTransition(
	ctx context.Context,
	transitions *transitionEmitter,
	kind TransitionType,
	state State,
	binding TransitionToolBinding,
	parentTransitionID string,
	outcome string,
	private TransitionPrivateData,
) (Transition, *RunError) {
	if !transitions.enabled() {
		return Transition{}, nil
	}
	value, err := transitions.commit(ctx, kind, state, transitionFields{
		parentTransitionID: parentTransitionID,
		callID:             binding.CallID,
		idempotencyKey:     binding.IdempotencyKey,
		outcome:            outcome,
		tools:              []TransitionToolBinding{binding},
		private:            private,
	})
	if err != nil {
		return value, transitionRunError("tool_effect_transition_failed", err)
	}
	return value, nil
}

func (a *Agent) appendToolResultTransition(
	ctx context.Context,
	transitions *transitionEmitter,
	state *State,
	call message.ToolCall,
	result tool.Result,
	cause string,
	parentTransitionID string,
) *RunError {
	next := state.Clone()
	if err := a.appendToolResult(&next, call, result); err != nil {
		return &RunError{Class: ErrorInternal, Code: "invalid_state", SafeMessage: "The Agent could not record a Tool result.", Cause: err}
	}
	return a.commitStateTransition(ctx, transitions, state, next, cause, parentTransitionID)
}

func (a *Agent) appendToolErrorTransition(
	ctx context.Context,
	transitions *transitionEmitter,
	state *State,
	call message.ToolCall,
	code string,
	safeMessage string,
	retryable bool,
	cause string,
	parentTransitionID string,
) *RunError {
	next := state.Clone()
	if err := a.appendToolError(&next, call, code, safeMessage, retryable); err != nil {
		return &RunError{Class: ErrorInternal, Code: "invalid_state", SafeMessage: "The Agent could not record a Tool error.", Cause: err}
	}
	return a.commitStateTransition(ctx, transitions, state, next, cause, parentTransitionID)
}
