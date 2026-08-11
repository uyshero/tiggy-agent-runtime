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
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

type recordingTransitionHook struct {
	mu       sync.Mutex
	values   []agent.Transition
	failType agent.TransitionType
	failOnce bool
	panicAt  agent.TransitionType
}

func (h *recordingTransitionHook) CommitTransition(_ context.Context, value agent.Transition) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.values = append(h.values, value.Clone())
	if value.Type == h.panicAt {
		panic("transition hook panic")
	}
	if value.Type == h.failType {
		if h.failOnce {
			h.failType = ""
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
