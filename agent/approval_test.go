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
	"github.com/uyshero/tiggy-agent-runtime/approval"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestApprovalSuspendsWholeBatchAndResumesExactCalls(t *testing.T) {
	t.Parallel()

	executions := 0
	policyCalls := 0
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: twoToolCallResponse()},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				if countToolResults(request.Messages) != 2 {
					return fmt.Errorf("tool result count = %d", countToolResults(request.Messages))
				}
				return nil
			},
			Response: textResponse("done"),
		},
	)
	policy := approval.PolicyFunc(func(_ context.Context, request approval.Request) (approval.Decision, error) {
		policyCalls++
		if request.CallID == "call_2" {
			return approval.Decision{Behavior: approval.BehaviorRequire, Reason: "filesystem write"}, nil
		}
		return approval.Decision{Behavior: approval.BehaviorAllow}, nil
	})
	value := newApprovalAgent(t, script, policy, &executions)
	firstTrace := &agenttest.Trace{}

	first, err := value.RunStream(context.Background(), userMessage("run both"), firstTrace.Sink)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != agent.StatusSuspended || first.Suspension == nil || executions != 0 || policyCalls != 2 {
		t.Fatalf("first result = %#v executions=%d policy_calls=%d", first, executions, policyCalls)
	}
	if err := first.State.ValidateSuspended(); err != nil {
		t.Fatalf("suspended State: %v", err)
	}
	if len(first.Suspension.Calls) != 2 || first.Suspension.Calls[1].ApprovalID == "" {
		t.Fatalf("suspension = %#v", first.Suspension)
	}
	if containsEvent(first.Events, event.ToolCallStarted) {
		t.Fatal("Tool execution started before the batch was approved")
	}
	if err := agenttest.ValidateTrace(firstTrace.Events()); err != nil {
		t.Fatalf("suspended trace: %v", err)
	}

	secondTrace := &agenttest.Trace{}
	second, err := value.Resume(context.Background(), first.State, []approval.Resolution{{
		ApprovalID: first.Suspension.Calls[1].ApprovalID,
		Status:     approval.ResolutionApproved,
	}}, secondTrace.Sink)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != agent.StatusCompleted || second.Final == nil || executions != 2 || policyCalls != 2 {
		t.Fatalf("second result = %#v executions=%d policy_calls=%d", second, executions, policyCalls)
	}
	if err := agenttest.ValidateTrace(secondTrace.Events()); err != nil {
		t.Fatalf("resumed trace: %v", err)
	}
	started, resolved := second.Events[0], approvalFindEvent(second.Events, event.ApprovalResolved)
	var startedPayload event.RunStartedPayload
	if json.Unmarshal(started.Payload, &startedPayload) != nil || startedPayload.ContinuationID != first.Suspension.ID || resolved == nil {
		t.Fatalf("resume provenance missing: started=%s resolved=%#v", started.Payload, resolved)
	}
}

func TestApprovalRejectionReturnsSafeToolResultWithoutExecution(t *testing.T) {
	t.Parallel()

	executions := 0
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				result := lastToolResult(request.Messages)
				if result == nil || !result.IsError || result.Retryable {
					return fmt.Errorf("rejection result = %#v", result)
				}
				var state map[string]string
				if json.Unmarshal(result.State, &state) != nil || state["code"] != "approval_rejected" {
					return fmt.Errorf("rejection state = %s", result.State)
				}
				return nil
			},
			Response: textResponse("not executed"),
		},
	)
	value := newApprovalAgent(t, script, approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
		return approval.Decision{Behavior: approval.BehaviorRequire, Reason: "destructive"}, nil
	}), &executions)
	first, err := value.Run(context.Background(), userMessage("effect"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := value.Resume(context.Background(), first.State, []approval.Resolution{{
		ApprovalID: first.Suspension.Calls[0].ApprovalID, Status: approval.ResolutionRejected, Reason: "outside change window",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != agent.StatusCompleted || executions != 0 {
		t.Fatalf("result = %#v executions=%d", second, executions)
	}
}

func TestInvalidApprovalResolutionPreservesSuspension(t *testing.T) {
	t.Parallel()

	executions := 0
	value := newApprovalAgent(t,
		agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")}),
		approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
			return approval.Decision{Behavior: approval.BehaviorRequire}, nil
		}),
		&executions,
	)
	first, err := value.Run(context.Background(), userMessage("effect"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := value.Resume(context.Background(), first.State, nil, nil)
	assertRunError(t, err, agent.ErrorInvalidInput, "invalid_approval_resolution")
	if second.Status != agent.StatusSuspended || second.Suspension == nil || second.Suspension.ID != first.Suspension.ID || executions != 0 {
		t.Fatalf("invalid resolution consumed suspension: %#v executions=%d", second, executions)
	}
	if err := agenttest.ValidateTrace(second.Events); err != nil {
		t.Fatalf("invalid resolution trace: %v", err)
	}
}

func TestApprovalResumeRejectsMutatedToolArguments(t *testing.T) {
	t.Parallel()

	executions := 0
	value := newApprovalAgent(t,
		agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")}),
		approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
			return approval.Decision{Behavior: approval.BehaviorRequire}, nil
		}),
		&executions,
	)
	first, err := value.Run(context.Background(), userMessage("effect"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := first.State.Clone()
	for messageIndex := range tampered.Messages {
		for contentIndex := range tampered.Messages[messageIndex].Content {
			if call := tampered.Messages[messageIndex].Content[contentIndex].ToolCall; call != nil {
				call.Arguments = json.RawMessage(`{"changed":true}`)
			}
		}
	}
	result, err := value.Resume(context.Background(), tampered, []approval.Resolution{{
		ApprovalID: first.Suspension.Calls[0].ApprovalID, Status: approval.ResolutionApproved,
	}}, nil)
	assertRunError(t, err, agent.ErrorInvalidInput, "invalid_suspended_state")
	if result.Status != agent.StatusFailed || executions != 0 {
		t.Fatalf("tampered resume = %#v executions=%d", result, executions)
	}
}

func TestApprovalResumeRejectsChangedToolDefinition(t *testing.T) {
	t.Parallel()

	executions := 0
	firstAgent := newApprovalAgent(t,
		agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "effect")}),
		approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
			return approval.Decision{Behavior: approval.BehaviorRequire}, nil
		}),
		&executions,
	)
	first, err := firstAgent.Run(context.Background(), userMessage("effect"))
	if err != nil {
		t.Fatal(err)
	}

	changedDefinition := model.ToolDefinition{
		Name:        "effect",
		Description: "changed after suspension",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	resumeAgent := newApprovalAgentWithDefinition(t, agenttest.NewScriptedModel(), nil, &executions, changedDefinition)
	result, err := resumeAgent.Resume(context.Background(), first.State, []approval.Resolution{{
		ApprovalID: first.Suspension.Calls[0].ApprovalID, Status: approval.ResolutionApproved,
	}}, nil)
	assertRunError(t, err, agent.ErrorInvalidInput, "approval_toolkit_mismatch")
	if result.Status != agent.StatusSuspended || result.Suspension == nil || result.Suspension.ID != first.Suspension.ID || executions != 0 {
		t.Fatalf("changed definition resume = %#v executions=%d", result, executions)
	}
	if containsEvent(result.Events, event.ApprovalResolved) || containsEvent(result.Events, event.ToolCallStarted) {
		t.Fatalf("changed definition consumed approval or started a Tool: %v", agenttest.EventTypes(result.Events))
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatalf("changed definition trace: %v", err)
	}
}

func TestApprovalPolicyFailurePreventsWholeBatch(t *testing.T) {
	t.Parallel()

	executions := 0
	value := newApprovalAgent(t, agenttest.NewScriptedModel(agenttest.ModelStep{Response: twoToolCallResponse()}),
		approval.PolicyFunc(func(_ context.Context, request approval.Request) (approval.Decision, error) {
			if request.CallID == "call_2" {
				return approval.Decision{}, errors.New("policy unavailable")
			}
			return approval.Decision{Behavior: approval.BehaviorAllow}, nil
		}), &executions)
	result, err := value.Run(context.Background(), userMessage("run both"))
	assertRunError(t, err, agent.ErrorApproval, "approval_policy_failed")
	if result.Status != agent.StatusFailed || executions != 0 {
		t.Fatalf("result = %#v executions=%d", result, executions)
	}
}

func newApprovalAgent(t *testing.T, modelValue model.Model, policy approval.Policy, executions *int) *agent.Agent {
	t.Helper()
	return newApprovalAgentWithDefinition(t, modelValue, policy, executions, model.ToolDefinition{
		Name: "effect", InputSchema: json.RawMessage(`{"type":"object"}`),
	})
}

func newApprovalAgentWithDefinition(
	t *testing.T,
	modelValue model.Model,
	policy approval.Policy,
	executions *int,
	definition model.ToolDefinition,
) *agent.Agent {
	t.Helper()
	effect := agenttest.FuncTool{
		DefinitionValue: definition,
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			(*executions)++
			return tool.TextResult("executed"), nil
		},
	}
	kit, err := toolkit.New(effect)
	if err != nil {
		t.Fatal(err)
	}
	value, err := agent.New(agent.Config{
		Name: "approval-test", Model: modelValue, ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		Toolkit: kit, ApprovalPolicy: policy, IDs: agenttest.SequentialIDs(), Limits: agent.DefaultLimits(),
		Now: agenttest.StepClock(time.Unix(1_700_000_000, 0), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func twoToolCallResponse() model.Response {
	return model.Response{
		Message: message.Message{Content: []message.Content{
			{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call_1", Name: "effect", Arguments: json.RawMessage(`{"order":1}`)}},
			{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call_2", Name: "effect", Arguments: json.RawMessage(`{"order":2}`)}},
		}},
		StopReason: model.StopReasonToolCall,
	}
}

func containsEvent(events []event.Event, eventType event.Type) bool {
	return approvalFindEvent(events, eventType) != nil
}

func approvalFindEvent(events []event.Event, eventType event.Type) *event.Event {
	for index := range events {
		if events[index].Type == eventType {
			return &events[index]
		}
	}
	return nil
}
