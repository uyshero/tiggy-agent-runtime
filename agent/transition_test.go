package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/approval"
	"github.com/uyshero/tiggy-agent-runtime/artifact"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

type recordingTransitionHook struct {
	mu                      sync.Mutex
	values                  []agent.Transition
	failType                agent.TransitionType
	failCause               string
	failOnce                bool
	panicAt                 agent.TransitionType
	rejectDuplicateRecovery bool
	recoveryKeys            map[string]struct{}
}

func (h *recordingTransitionHook) CommitTransition(_ context.Context, value agent.Transition) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.values = append(h.values, value.Clone())
	if value.Type == agent.TransitionRecoveryContinuationApplied && h.rejectDuplicateRecovery {
		if h.recoveryKeys == nil {
			h.recoveryKeys = make(map[string]struct{})
		}
		if _, exists := h.recoveryKeys[value.IdempotencyKey]; exists {
			return errors.New("recovery continuation already applied")
		}
		h.recoveryKeys[value.IdempotencyKey] = struct{}{}
	}
	if value.Type == h.panicAt {
		panic("transition hook panic")
	}
	if value.Type == h.failType || (h.failCause != "" && value.Cause == h.failCause) {
		if h.failOnce {
			h.failType = ""
			h.failCause = ""
		}
		return errors.New("checkpoint unavailable")
	}
	return nil
}

func (h *recordingTransitionHook) transitions() []agent.Transition {
	h.mu.Lock()
	defer h.mu.Unlock()
	values := make([]agent.Transition, len(h.values))
	for index := range h.values {
		values[index] = h.values[index].Clone()
	}
	return values
}

func (h *recordingTransitionHook) failNext(kind agent.TransitionType) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failType = kind
	h.failOnce = true
}

func (h *recordingTransitionHook) failNextCause(cause string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failCause = cause
	h.failOnce = true
}

func TestTransitionHookRecordsDurableToolBoundariesWithoutContentMetadata(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	executions := 0
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: model.Response{
			Message: message.Message{Content: []message.Content{{
				Type: message.ContentToolCall,
				ToolCall: &message.ToolCall{
					ID: "call_1", Name: "effect", Arguments: json.RawMessage(`{"secret_argument":"must-not-leak"}`),
				},
			}}},
			StopReason: model.StopReasonToolCall,
		}},
		agenttest.ModelStep{Response: textResponse("done")},
	)
	var executionTransition agent.ToolExecutionTransition
	value := newTransitionAgentWithExecute(t, script, hook, nil, agent.ToolEffectReadOnly, func(ctx context.Context) {
		executions++
		var ok bool
		executionTransition, ok = agent.ToolTransitionFromContext(ctx)
		if !ok {
			t.Error("Tool execution context has no transition metadata")
		}
	})

	result, err := value.Run(context.Background(), userMessage("run effect"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted || executions != 1 {
		t.Fatalf("result = %#v, executions = %d", result, executions)
	}
	values := hook.transitions()
	wantTypes := []agent.TransitionType{
		agent.TransitionStateCommitted,
		agent.TransitionModelCallPrepared,
		agent.TransitionModelCallCompleted,
		agent.TransitionStateCommitted,
		agent.TransitionToolBatchFrozen,
		agent.TransitionToolEffectPrepared,
		agent.TransitionToolEffectStarted,
		agent.TransitionToolEffectCompleted,
		agent.TransitionStateCommitted,
		agent.TransitionModelCallPrepared,
		agent.TransitionModelCallCompleted,
		agent.TransitionStateCommitted,
	}
	if len(values) != len(wantTypes) {
		t.Fatalf("Transition count = %d, want %d: %v", len(values), len(wantTypes), transitionTypes(values))
	}
	for index, value := range values {
		if value.Type != wantTypes[index] || value.Sequence != int64(index+1) {
			t.Fatalf("Transition %d = %s/%d, want %s/%d", index, value.Type, value.Sequence, wantTypes[index], index+1)
		}
		if err := value.Validate(); err != nil {
			t.Fatalf("Transition %d: %v", index, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "must-not-leak") || strings.Contains(string(encoded), "secret-result") {
			t.Fatalf("Transition metadata leaked content: %s", encoded)
		}
	}
	frozen := findTransition(t, values, agent.TransitionToolBatchFrozen)
	if len(frozen.Tools) != 1 || frozen.Tools[0].EffectClass != agent.ToolEffectReadOnly || frozen.Tools[0].IdempotencyKey == "" {
		t.Fatalf("frozen binding = %#v", frozen.Tools)
	}
	if executionTransition.IdempotencyKey != frozen.Tools[0].IdempotencyKey || executionTransition.CallID != "call_1" {
		t.Fatalf("Tool execution transition = %#v, frozen = %#v", executionTransition, frozen.Tools[0])
	}
	modelPrepared := findTransition(t, values, agent.TransitionModelCallPrepared)
	if modelPrepared.Private.ModelRequest == nil || len(modelPrepared.Private.ModelRequest.Messages) == 0 {
		t.Fatalf("model prepared private payload = %#v", modelPrepared.Private)
	}
	var completedTool agent.Transition
	for _, value := range values {
		if value.Type == agent.TransitionToolEffectCompleted && value.Outcome == "succeeded" {
			completedTool = value
			break
		}
	}
	if completedTool.Private.ToolResult == nil || completedTool.Private.ToolResult.Content[0].Text != "secret-result" ||
		!strings.Contains(string(completedTool.Private.ToolArguments), "must-not-leak") {
		t.Fatalf("Tool completion private payload = %#v", completedTool.Private)
	}
	assertTerminalInvariants(t, result)
}

func TestTransitionHookFailureBeforeToolEffectPreventsExecution(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{failType: agent.TransitionToolEffectStarted}
	executions := 0
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")})
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectNonReconcilableWrite, &executions)

	result, err := value.Run(context.Background(), userMessage("run effect"))
	assertRunError(t, err, agent.ErrorTransition, "tool_effect_transition_failed")
	if result.Status != agent.StatusFailed || executions != 0 || len(script.Calls()) != 1 {
		t.Fatalf("result = %#v, executions = %d, model calls = %d", result, executions, len(script.Calls()))
	}
	assertTerminalInvariants(t, result)
}

func TestInitialStateTransitionFailurePreventsModelCall(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{failType: agent.TransitionStateCommitted}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("must not run")})
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectNonReconcilableWrite, nil)

	result, err := value.Run(context.Background(), userMessage("answer"))
	assertRunError(t, err, agent.ErrorTransition, "state_transition_failed")
	if result.Status != agent.StatusFailed || len(script.Calls()) != 0 || len(result.State.Messages) != 0 {
		t.Fatalf("result = %#v, model calls = %d", result, len(script.Calls()))
	}
	assertTerminalInvariants(t, result)
}

func TestModelCompletionTransitionFailureDoesNotCommitResponse(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{failType: agent.TransitionModelCallCompleted}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("uncommitted")})
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectNonReconcilableWrite, nil)

	result, err := value.Run(context.Background(), userMessage("answer"))
	assertRunError(t, err, agent.ErrorTransition, "model_call_complete_failed")
	if result.Status != agent.StatusFailed || len(script.Calls()) != 1 || len(result.State.Messages) != 1 {
		t.Fatalf("result = %#v, model calls = %d", result, len(script.Calls()))
	}
	assertTerminalInvariants(t, result)
}

func TestRecoverAppliesSavedModelResponseWithoutCallingModelAgain(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	hook.failNextCause("model_response_committed")
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("recovered answer")})
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectNonReconcilableWrite, nil)

	first, err := value.Run(context.Background(), userMessage("answer"))
	assertRunError(t, err, agent.ErrorTransition, "state_transition_failed")
	if first.Status != agent.StatusFailed || len(script.Calls()) != 1 {
		t.Fatalf("first result = %#v, model calls = %d", first, len(script.Calls()))
	}
	completed := findTransition(t, hook.transitions(), agent.TransitionModelCallCompleted)
	if completed.Private.ModelResponse == nil || completed.Private.ModelResponse.Message.ID == "" {
		t.Fatalf("saved response is not normalized: %#v", completed.Private.ModelResponse)
	}

	recovered, err := value.Recover(context.Background(), completed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != agent.StatusCompleted || recovered.Final == nil ||
		recovered.Final.Content[0].Text != "recovered answer" || len(script.Calls()) != 1 {
		t.Fatalf("recovered result = %#v, model calls = %d", recovered, len(script.Calls()))
	}
	applied := findTransition(t, hook.transitions(), agent.TransitionRecoveryContinuationApplied)
	if applied.ParentTransitionID != completed.ID || applied.ContinuationID != completed.ID || applied.Outcome != "model_response" {
		t.Fatalf("recovery transition = %#v", applied)
	}
	assertTerminalInvariants(t, recovered)
}

func TestRecoverContinuesSavedModelToolCallsThroughNormalPipeline(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	hook.failNextCause("model_response_committed")
	executions := 0
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")},
		agenttest.ModelStep{Response: textResponse("done")},
	)
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectIdempotentWrite, &executions)
	_, err := value.Run(context.Background(), userMessage("run effect"))
	assertRunError(t, err, agent.ErrorTransition, "state_transition_failed")
	if executions != 0 || len(script.Calls()) != 1 {
		t.Fatalf("failed invocation progressed: executions = %d, model calls = %d", executions, len(script.Calls()))
	}
	completed := findTransition(t, hook.transitions(), agent.TransitionModelCallCompleted)

	recovered, err := value.Recover(context.Background(), completed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != agent.StatusCompleted || executions != 1 || len(script.Calls()) != 2 {
		t.Fatalf("recovered result = %#v, executions = %d, model calls = %d", recovered, executions, len(script.Calls()))
	}
	assertTerminalInvariants(t, recovered)
}

func TestRecoverAppliesSavedToolResultWithoutReplayingTool(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	hook.failNextCause("tool_result_committed")
	executions := 0
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")},
		agenttest.ModelStep{Response: textResponse("done")},
	)
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectIdempotentWrite, &executions)

	first, err := value.Run(context.Background(), userMessage("run effect"))
	assertRunError(t, err, agent.ErrorTransition, "state_transition_failed")
	if first.Status != agent.StatusFailed || executions != 1 || len(script.Calls()) != 1 {
		t.Fatalf("first result = %#v, executions = %d, model calls = %d", first, executions, len(script.Calls()))
	}
	completed := findTransition(t, hook.transitions(), agent.TransitionToolEffectCompleted)
	if completed.Private.ToolResult == nil || !completed.Private.ToolResultStateReady {
		t.Fatalf("saved Tool result is not State-ready: %#v", completed.Private)
	}

	recovered, err := value.Recover(context.Background(), completed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != agent.StatusCompleted || executions != 1 || len(script.Calls()) != 2 {
		t.Fatalf("recovered result = %#v, executions = %d, model calls = %d", recovered, executions, len(script.Calls()))
	}
	projected := lastToolResult(recovered.State.Messages)
	if projected == nil || projected.Content[0].Text != "secret-result" {
		t.Fatalf("recovered Tool result = %#v", projected)
	}
	assertTerminalInvariants(t, recovered)
}

func TestRecoverPreservesSavedModelVisibleToolError(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	hook.failNextCause("tool_result_committed")
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				result := lastToolResult(request.Messages)
				if result == nil || !result.IsError || !result.Retryable || result.Content[0].Text != "retry safely" {
					return fmt.Errorf("recovered Tool error = %#v", result)
				}
				return nil
			},
			Response: textResponse("done"),
		},
	)
	kit, err := toolkit.New(agenttest.FuncTool{
		DefinitionValue: model.ToolDefinition{Name: "effect", InputSchema: json.RawMessage(`{"type":"object"}`)},
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{}, tool.NewError("temporary", "retry safely", true, nil)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := agent.New(agent.Config{
		Name: "recovery-tool-error", Model: script,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"}, Toolkit: kit,
		TransitionHook: hook, ToolEffectClasses: map[string]agent.ToolEffectClass{"effect": agent.ToolEffectReadOnly},
		IDs: agenttest.SequentialIDs(), Now: agenttest.StepClock(time.Unix(1_700_000_000, 0), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = value.Run(context.Background(), userMessage("run effect"))
	assertRunError(t, err, agent.ErrorTransition, "state_transition_failed")
	completed := findTransition(t, hook.transitions(), agent.TransitionToolEffectCompleted)
	if completed.Outcome != "tool_error" || !completed.Private.ToolResultIsError || !completed.Private.ToolResultRetryable {
		t.Fatalf("saved Tool error = %#v", completed)
	}
	recovered, err := value.Recover(context.Background(), completed, nil)
	if err != nil || recovered.Status != agent.StatusCompleted {
		t.Fatalf("recovered result = %#v, error = %v", recovered, err)
	}
}

func TestRecoverUsesSavedArtifactProjectionWithoutOffloadingAgain(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	hook.failNextCause("tool_result_committed")
	storeCalls := 0
	offloader := newAgentOffloader(t, agentStoreFunc(func(_ context.Context, request artifact.PutRequest) (artifact.StoredObject, error) {
		storeCalls++
		return artifact.StoredObject{ObjectRefID: "objects/recovered-result"}, nil
	}), 32, 4096)
	largeResult := strings.Repeat("recoverable-", 64)
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "report")},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				result := lastToolResult(request.Messages)
				if result == nil || len(result.Content) != 1 || result.Content[0].Artifact == nil ||
					result.Content[0].Artifact.ObjectRefID != "objects/recovered-result" {
					return fmt.Errorf("recovered Artifact result = %#v", result)
				}
				return nil
			},
			Response: textResponse("done"),
		},
	)
	kit, err := toolkit.New(agenttest.FuncTool{
		DefinitionValue: model.ToolDefinition{Name: "report", InputSchema: json.RawMessage(`{"type":"object"}`)},
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.TextResult(largeResult), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := agent.New(agent.Config{
		Name: "recovery-artifact", Model: script,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"}, Toolkit: kit,
		ArtifactOffloader: offloader, TransitionHook: hook,
		ToolEffectClasses: map[string]agent.ToolEffectClass{"report": agent.ToolEffectReadOnly},
		IDs:               agenttest.SequentialIDs(), Now: agenttest.StepClock(time.Unix(1_700_000_000, 0), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = value.Run(context.Background(), userMessage("create report"))
	assertRunError(t, err, agent.ErrorTransition, "state_transition_failed")
	completed := findTransition(t, hook.transitions(), agent.TransitionToolEffectCompleted)
	if completed.Private.ToolResult == nil || !completed.Private.ToolResultStateReady ||
		completed.Private.ToolResult.Content[0].Artifact == nil {
		t.Fatalf("saved projected result = %#v", completed.Private)
	}
	recovered, err := value.Recover(context.Background(), completed, nil)
	if err != nil || recovered.Status != agent.StatusCompleted {
		t.Fatalf("recovered result = %#v, error = %v", recovered, err)
	}
	if storeCalls != 1 {
		t.Fatalf("Artifact store calls = %d, want 1", storeCalls)
	}
}

func TestRecoverSuspendsRemainingToolBatchInsteadOfExecutingIt(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	hook.failNextCause("tool_result_committed")
	executions := 0
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: model.Response{
			Message: message.Message{Content: []message.Content{
				{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call_1", Name: "effect", Arguments: json.RawMessage(`{}`)}},
				{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call_2", Name: "effect", Arguments: json.RawMessage(`{}`)}},
			}},
			StopReason: model.StopReasonToolCall,
		}},
		agenttest.ModelStep{Response: textResponse("done")},
	)
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectIdempotentWrite, &executions)

	_, err := value.Run(context.Background(), userMessage("run both"))
	assertRunError(t, err, agent.ErrorTransition, "state_transition_failed")
	if executions != 1 {
		t.Fatalf("executions before recovery = %d", executions)
	}
	completed := findTransition(t, hook.transitions(), agent.TransitionToolEffectCompleted)

	recovered, err := value.Recover(context.Background(), completed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != agent.StatusSuspended || recovered.Suspension == nil || executions != 1 {
		t.Fatalf("recovered result = %#v, executions = %d", recovered, executions)
	}
	if len(recovered.Suspension.Calls) != 1 || recovered.Suspension.Calls[0].CallID != "call_2" ||
		recovered.Suspension.Calls[0].Behavior != approval.BehaviorRequire {
		t.Fatalf("recovery suspension = %#v", recovered.Suspension)
	}
	if err := recovered.State.ValidateSuspended(); err != nil {
		t.Fatalf("recovery suspension is invalid: %v", err)
	}
}

func TestRecoverRejectsTamperedToolArgumentsAndHookFailureIsFailClosed(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	hook.failNextCause("tool_result_committed")
	executions := 0
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")},
		agenttest.ModelStep{Response: textResponse("must not run")},
	)
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectReconcilableWrite, &executions)
	_, err := value.Run(context.Background(), userMessage("run effect"))
	assertRunError(t, err, agent.ErrorTransition, "state_transition_failed")
	completed := findTransition(t, hook.transitions(), agent.TransitionToolEffectCompleted)

	tampered := completed.Clone()
	tampered.Private.ToolArguments = json.RawMessage(`{"changed":true}`)
	_, err = value.Recover(context.Background(), tampered, nil)
	assertRunError(t, err, agent.ErrorInvalidInput, "recovery_arguments_mismatch")
	if executions != 1 || len(script.Calls()) != 1 {
		t.Fatalf("tampered recovery progressed: executions = %d, model calls = %d", executions, len(script.Calls()))
	}
	indeterminate := completed.Clone()
	indeterminate.Type = agent.TransitionToolEffectIndeterminate
	indeterminate.Outcome = "operator_reconciled"
	_, err = value.Recover(context.Background(), indeterminate, nil)
	assertRunError(t, err, agent.ErrorInvalidInput, "unsupported_recovery_transition")

	withoutHook := newTransitionAgent(t, agenttest.NewScriptedModel(), nil, nil, agent.ToolEffectReconcilableWrite, new(int))
	_, err = withoutHook.Recover(context.Background(), completed, nil)
	assertRunError(t, err, agent.ErrorInvalidInput, "recovery_hook_required")

	hook.failNext(agent.TransitionRecoveryContinuationApplied)
	failed, err := value.Recover(context.Background(), completed, nil)
	assertRunError(t, err, agent.ErrorTransition, "recovery_continuation_failed")
	if failed.Status != agent.StatusFailed || executions != 1 || len(script.Calls()) != 1 {
		t.Fatalf("failed recovery progressed: %#v, executions = %d, model calls = %d", failed, executions, len(script.Calls()))
	}
}

func TestRecoverUsesStableIdempotencyKeyForDuplicateSubmission(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{rejectDuplicateRecovery: true}
	hook.failNextCause("model_response_committed")
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("done")})
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectNonReconcilableWrite, nil)
	_, err := value.Run(context.Background(), userMessage("answer"))
	assertRunError(t, err, agent.ErrorTransition, "state_transition_failed")
	completed := findTransition(t, hook.transitions(), agent.TransitionModelCallCompleted)

	first, err := value.Recover(context.Background(), completed, nil)
	if err != nil || first.Status != agent.StatusCompleted {
		t.Fatalf("first recovery = %#v, error = %v", first, err)
	}
	second, err := value.Recover(context.Background(), completed, nil)
	assertRunError(t, err, agent.ErrorTransition, "recovery_continuation_failed")
	if second.Status != agent.StatusFailed || len(script.Calls()) != 1 {
		t.Fatalf("duplicate recovery = %#v, model calls = %d", second, len(script.Calls()))
	}
	var keys []string
	for _, transition := range hook.transitions() {
		if transition.Type == agent.TransitionRecoveryContinuationApplied {
			keys = append(keys, transition.IdempotencyKey)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("recovery idempotency keys = %#v", keys)
	}
}

func TestToolBatchFreezeFailurePreventsEffects(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{failType: agent.TransitionToolBatchFrozen}
	executions := 0
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")})
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectNonReconcilableWrite, &executions)

	result, err := value.Run(context.Background(), userMessage("run effect"))
	assertRunError(t, err, agent.ErrorTransition, "tool_batch_freeze_failed")
	if result.Status != agent.StatusFailed || executions != 0 || len(script.Calls()) != 1 {
		t.Fatalf("result = %#v, executions = %d, model calls = %d", result, executions, len(script.Calls()))
	}
	assertTerminalInvariants(t, result)
}

func TestUnpersistedToolCompletionBecomesIndeterminateAndIsNotReplayed(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{failType: agent.TransitionToolEffectCompleted, failOnce: true}
	executions := 0
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")},
		agenttest.ModelStep{Response: textResponse("must not run")},
	)
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectNonReconcilableWrite, &executions)

	result, err := value.Run(context.Background(), userMessage("run effect"))
	assertRunError(t, err, agent.ErrorTransition, "tool_effect_transition_failed")
	if result.Status != agent.StatusFailed || executions != 1 || len(script.Calls()) != 1 {
		t.Fatalf("result = %#v, executions = %d, model calls = %d", result, executions, len(script.Calls()))
	}
	values := hook.transitions()
	if findTransition(t, values, agent.TransitionToolEffectIndeterminate).Outcome != "completion_unpersisted" {
		t.Fatalf("Transitions = %v", transitionTypes(values))
	}
	assertTerminalInvariants(t, result)
}

func TestResumeTransitionFailurePreservesContinuation(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	executions := 0
	policy := approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
		return approval.Decision{Behavior: approval.BehaviorRequire}, nil
	})
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")})
	value := newTransitionAgent(t, script, hook, policy, agent.ToolEffectReconcilableWrite, &executions)
	first, err := value.Run(context.Background(), userMessage("run effect"))
	if err != nil {
		t.Fatal(err)
	}
	hook.failNext(agent.TransitionResumeContinuationConsumed)
	second, err := value.Resume(context.Background(), first.State, []approval.Resolution{{
		ApprovalID: first.Suspension.Calls[0].ApprovalID,
		Status:     approval.ResolutionApproved,
	}}, nil)
	assertRunError(t, err, agent.ErrorTransition, "resume_continuation_failed")
	if second.Status != agent.StatusSuspended || second.Suspension == nil ||
		second.Suspension.ID != first.Suspension.ID || executions != 0 {
		t.Fatalf("resume consumed continuation: %#v, executions = %d", second, executions)
	}
	if err := second.State.ValidateSuspended(); err != nil {
		t.Fatalf("preserved suspension: %v", err)
	}
}

func TestToolIdempotencyKeySurvivesApprovalResume(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	executions := 0
	policy := approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
		return approval.Decision{Behavior: approval.BehaviorRequire}, nil
	})
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")},
		agenttest.ModelStep{Response: textResponse("done")},
	)
	value := newTransitionAgent(t, script, hook, policy, agent.ToolEffectIdempotentWrite, &executions)
	first, err := value.Run(context.Background(), userMessage("run effect"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := value.Resume(context.Background(), first.State, []approval.Resolution{{
		ApprovalID: first.Suspension.Calls[0].ApprovalID,
		Status:     approval.ResolutionApproved,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != agent.StatusCompleted || executions != 1 {
		t.Fatalf("result = %#v, executions = %d", second, executions)
	}
	var frozen []agent.Transition
	for _, value := range hook.transitions() {
		if value.Type == agent.TransitionToolBatchFrozen {
			frozen = append(frozen, value)
		}
	}
	if len(frozen) != 2 || frozen[0].Tools[0].IdempotencyKey != frozen[1].Tools[0].IdempotencyKey ||
		frozen[0].BatchDigest != frozen[1].BatchDigest {
		t.Fatalf("frozen batches changed across Resume: %#v", frozen)
	}
}

func TestApprovalResumeRejectsChangedToolEffectClass(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	executions := 0
	policy := approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
		return approval.Decision{Behavior: approval.BehaviorRequire}, nil
	})
	firstAgent := newTransitionAgent(t,
		agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")}),
		hook, policy, agent.ToolEffectReconcilableWrite, &executions,
	)
	first, err := firstAgent.Run(context.Background(), userMessage("run effect"))
	if err != nil {
		t.Fatal(err)
	}
	resumeAgent := newTransitionAgent(t, agenttest.NewScriptedModel(), hook, nil, agent.ToolEffectReadOnly, &executions)
	second, err := resumeAgent.Resume(context.Background(), first.State, []approval.Resolution{{
		ApprovalID: first.Suspension.Calls[0].ApprovalID,
		Status:     approval.ResolutionApproved,
	}}, nil)
	assertRunError(t, err, agent.ErrorInvalidInput, "approval_toolkit_mismatch")
	if second.Status != agent.StatusSuspended || second.Suspension == nil ||
		second.Suspension.ID != first.Suspension.ID || executions != 0 {
		t.Fatalf("changed effect class resumed Tool: %#v, executions = %d", second, executions)
	}
}

func TestLegacyApprovalSuspensionWithoutEffectBindingStillResumes(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{}
	executions := 0
	policy := approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
		return approval.Decision{Behavior: approval.BehaviorRequire}, nil
	})
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")},
		agenttest.ModelStep{Response: textResponse("done")},
	)
	value := newTransitionAgent(t, script, hook, policy, agent.ToolEffectIdempotentWrite, &executions)
	first, err := value.Run(context.Background(), userMessage("run effect"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := first.State.Clone()
	legacy.Suspension.Calls[0].EffectClass = ""
	legacy.Suspension.Calls[0].IdempotencyKey = ""
	second, err := value.Resume(context.Background(), legacy, []approval.Resolution{{
		ApprovalID: first.Suspension.Calls[0].ApprovalID,
		Status:     approval.ResolutionApproved,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != agent.StatusCompleted || executions != 1 {
		t.Fatalf("legacy Resume = %#v, executions = %d", second, executions)
	}
}

func TestTransitionHookPanicBeforeModelCallIsFailClosed(t *testing.T) {
	t.Parallel()

	hook := &recordingTransitionHook{panicAt: agent.TransitionModelCallPrepared}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: textResponse("must not run")})
	value := newTransitionAgent(t, script, hook, nil, agent.ToolEffectNonReconcilableWrite, nil)

	result, err := value.Run(context.Background(), userMessage("answer"))
	assertRunError(t, err, agent.ErrorTransition, "model_call_prepare_failed")
	if result.Status != agent.StatusFailed || len(script.Calls()) != 0 {
		t.Fatalf("result = %#v, model calls = %d", result, len(script.Calls()))
	}
	assertTerminalInvariants(t, result)
}

func TestStateDigestIsStableAndSensitiveToOwnedState(t *testing.T) {
	t.Parallel()

	state := agent.State{Messages: []message.Message{{
		ID: "msg_1", Role: message.RoleUser, Visibility: message.VisibilityPublic,
		Content: []message.Content{{Type: message.ContentText, Text: "hello"}},
	}}}
	first, err := state.Digest()
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.Clone().Digest()
	if err != nil || first != second {
		t.Fatalf("stable digest = %q/%q, err = %v", first, second, err)
	}
	state.Messages[0].Content[0].Text = "changed"
	changed, err := state.Digest()
	if err != nil || changed == first {
		t.Fatalf("changed digest = %q, original = %q, err = %v", changed, first, err)
	}
}

func TestInvalidToolEffectClassIsRejected(t *testing.T) {
	t.Parallel()

	_, err := agent.New(agent.Config{
		Name: "invalid-effect", Model: agenttest.NewScriptedModel(),
		ModelIdentity:     model.Identity{Provider: "test", Model: "scripted"},
		ToolEffectClasses: map[string]agent.ToolEffectClass{"effect": "unknown"},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported Tool effect class") {
		t.Fatalf("New() error = %v", err)
	}
}

func newTransitionAgent(
	t *testing.T,
	modelValue model.Model,
	hook agent.TransitionHook,
	policy approval.Policy,
	effectClass agent.ToolEffectClass,
	executions *int,
) *agent.Agent {
	t.Helper()
	var execute func(context.Context)
	if executions != nil {
		execute = func(context.Context) { (*executions)++ }
	}
	return newTransitionAgentWithExecute(t, modelValue, hook, policy, effectClass, execute)
}

func newTransitionAgentWithExecute(
	t *testing.T,
	modelValue model.Model,
	hook agent.TransitionHook,
	policy approval.Policy,
	effectClass agent.ToolEffectClass,
	execute func(context.Context),
) *agent.Agent {
	t.Helper()
	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	effectClasses := map[string]agent.ToolEffectClass(nil)
	if execute != nil {
		if err := kit.Register(agenttest.FuncTool{
			DefinitionValue: model.ToolDefinition{
				Name: "effect", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			},
			ExecuteFunc: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
				execute(ctx)
				return tool.TextResult("secret-result"), nil
			},
		}); err != nil {
			t.Fatal(err)
		}
		effectClasses = map[string]agent.ToolEffectClass{"effect": effectClass}
	}
	value, err := agent.New(agent.Config{
		Name: "transition-test", Model: modelValue,
		ModelIdentity:     model.Identity{Provider: "test", Model: "scripted"},
		Toolkit:           kit,
		ApprovalPolicy:    policy,
		TransitionHook:    hook,
		ToolEffectClasses: effectClasses,
		Limits:            agent.DefaultLimits(),
		IDs:               agenttest.SequentialIDs(),
		Now:               agenttest.StepClock(time.Unix(1_700_000_000, 0), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func transitionTypes(values []agent.Transition) []agent.TransitionType {
	types := make([]agent.TransitionType, len(values))
	for index, value := range values {
		types[index] = value.Type
	}
	return types
}

func findTransition(t *testing.T, values []agent.Transition, kind agent.TransitionType) agent.Transition {
	t.Helper()
	for _, value := range values {
		if value.Type == kind {
			return value
		}
	}
	t.Fatalf("Transition %s not found in %s", kind, fmt.Sprint(transitionTypes(values)))
	return agent.Transition{}
}
