package approval_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/approval"
)

func TestEvaluateDefaultsToAllowAndCopiesArguments(t *testing.T) {
	t.Parallel()
	request := approval.Request{CallID: "call_1", Tool: "builtin.edit", Arguments: json.RawMessage(`{"path":"a"}`)}
	decision, err := approval.Evaluate(context.Background(), nil, request)
	if err != nil || decision.Behavior != approval.BehaviorAllow {
		t.Fatalf("Evaluate() = %#v, %v", decision, err)
	}
	policy := approval.PolicyFunc(func(_ context.Context, received approval.Request) (approval.Decision, error) {
		received.Arguments[0] = '['
		return approval.Decision{Behavior: approval.BehaviorRequire, Reason: " review "}, nil
	})
	decision, err = approval.Evaluate(context.Background(), policy, request)
	if err != nil || decision.Behavior != approval.BehaviorRequire || decision.Reason != "review" || request.Arguments[0] != '{' {
		t.Fatalf("Evaluate() = %#v, %v; request=%s", decision, err, request.Arguments)
	}
}

func TestEvaluateContainsPolicyFailure(t *testing.T) {
	t.Parallel()
	request := approval.Request{CallID: "call_1", Tool: "tool", Arguments: json.RawMessage(`{}`)}
	for name, policy := range map[string]approval.Policy{
		"error": approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
			return approval.Decision{}, errors.New("unavailable")
		}),
		"panic": approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
			panic("boom")
		}),
		"invalid": approval.PolicyFunc(func(context.Context, approval.Request) (approval.Decision, error) {
			return approval.Decision{Behavior: "later"}, nil
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := approval.Evaluate(context.Background(), policy, request); !approval.IsPolicyFailure(err) {
				t.Fatalf("Evaluate() error = %v", err)
			}
		})
	}
}

func TestSuspensionValidation(t *testing.T) {
	t.Parallel()
	value := approval.Suspension{ID: "suspend_1", TurnStart: 1, Calls: []approval.PendingCall{{
		CallID: "call_1", Tool: "edit", ArgumentsDigest: approval.ArgumentsDigest("edit", json.RawMessage(`{}`)),
		ToolDigest: approval.ArgumentsDigest("definition", json.RawMessage(`{}`)),
		Behavior:   approval.BehaviorRequire, ApprovalID: "approval_1", Reason: "filesystem write",
	}}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := value.Clone()
	clone.Calls[0].CallID = "changed"
	if value.Calls[0].CallID != "call_1" {
		t.Fatal("Clone exposed mutable Calls")
	}
}
