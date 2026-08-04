// Package approval 定义进程内 Tool 审批决策，以及挂起/恢复 Agent Run 所需的可序列化续跑数据。
package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
)

const maxReasonBytes = 2048

// Behavior 是赋予一次 Tool Call 的执行处置。
type Behavior string

const (
	BehaviorAllow   Behavior = "allow"
	BehaviorDeny    Behavior = "deny"
	BehaviorRequire Behavior = "require_approval"
)

func (b Behavior) Validate() error {
	switch b {
	case BehaviorAllow, BehaviorDeny, BehaviorRequire:
		return nil
	default:
		return fmt.Errorf("unsupported approval behavior %q", b)
	}
}

// Request 在模型产出的批次中任一 Tool 执行前交给 Policy。
// Arguments 会被拷贝，且不会进入 Runtime Events。
type Request struct {
	CallID    string
	Tool      string
	Arguments json.RawMessage
}

func (r Request) Clone() Request {
	r.Arguments = append(json.RawMessage(nil), r.Arguments...)
	return r
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.CallID) == "" || strings.TrimSpace(r.Tool) == "" {
		return fmt.Errorf("approval call id and tool are required")
	}
	if len(r.CallID) > 256 || len(r.Tool) > 128 {
		return fmt.Errorf("approval call id or tool exceeds its size limit")
	}
	if len(r.Arguments) == 0 || !json.Valid(r.Arguments) {
		return fmt.Errorf("approval arguments must be valid JSON")
	}
	return nil
}

// Decision is one complete policy evaluation. Reason must be safe to expose to
// an operator and the model; it must not contain credentials or raw arguments.
type Decision struct {
	Behavior Behavior
	Reason   string
}

func (d Decision) Validate() error {
	if err := d.Behavior.Validate(); err != nil {
		return err
	}
	if len(d.Reason) > maxReasonBytes {
		return fmt.Errorf("approval reason exceeds %d bytes", maxReasonBytes)
	}
	return nil
}

// Policy evaluates Tool Calls. Implementations must be safe for concurrent use
// when one Policy is shared by concurrently reused Agents.
type Policy interface {
	Decide(context.Context, Request) (Decision, error)
}

type PolicyFunc func(context.Context, Request) (Decision, error)

func (f PolicyFunc) Decide(ctx context.Context, request Request) (Decision, error) {
	if f == nil {
		return Decision{}, fmt.Errorf("approval policy function is nil")
	}
	return f(ctx, request)
}

// AllowAll is the default-compatible policy for applications that do not need
// a human approval boundary.
type AllowAll struct{}

func (AllowAll) Decide(context.Context, Request) (Decision, error) {
	return Decision{Behavior: BehaviorAllow}, nil
}

type ErrorCode string

const (
	ErrorInvalidRequest ErrorCode = "invalid_approval_request"
	ErrorPolicyFailed   ErrorCode = "approval_policy_failed"
)

type Error struct {
	Code    ErrorCode
	Request Request
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return "approval evaluation failed"
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Evaluate validates and contains Policy failures. A nil Policy allows the
// call, preserving the behavior of Agent configurations created before the
// approval extension existed.
func Evaluate(ctx context.Context, policy Policy, request Request) (decision Decision, err error) {
	if ctx == nil {
		return Decision{}, &Error{Code: ErrorInvalidRequest, Request: request, Cause: fmt.Errorf("context is nil")}
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if err := request.Validate(); err != nil {
		return Decision{}, &Error{Code: ErrorInvalidRequest, Request: request, Cause: err}
	}
	if policy == nil {
		return Decision{Behavior: BehaviorAllow}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &Error{
				Code: ErrorPolicyFailed, Request: request,
				Cause: fmt.Errorf("approval policy panic: %v\n%s", recovered, debug.Stack()),
			}
		}
	}()
	decision, policyErr := policy.Decide(ctx, request.Clone())
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Decision{}, ctxErr
	}
	if policyErr != nil {
		return Decision{}, &Error{Code: ErrorPolicyFailed, Request: request, Cause: policyErr}
	}
	decision.Reason = strings.TrimSpace(decision.Reason)
	if err := decision.Validate(); err != nil {
		return Decision{}, &Error{Code: ErrorPolicyFailed, Request: request, Cause: err}
	}
	return decision, nil
}

// PendingCall binds a policy disposition to the exact Tool Call persisted in
// Agent State. ArgumentsDigest detects mutation before Resume.
type PendingCall struct {
	CallID          string   `json:"call_id"`
	Tool            string   `json:"tool"`
	ArgumentsDigest string   `json:"arguments_digest"`
	ToolDigest      string   `json:"tool_digest"`
	Behavior        Behavior `json:"behavior"`
	ApprovalID      string   `json:"approval_id,omitempty"`
	Reason          string   `json:"reason,omitempty"`
}

func (p PendingCall) Validate() error {
	if strings.TrimSpace(p.CallID) == "" || strings.TrimSpace(p.Tool) == "" || !ValidDigest(p.ArgumentsDigest) || !ValidDigest(p.ToolDigest) {
		return fmt.Errorf("pending approval call identity is invalid")
	}
	if err := p.Behavior.Validate(); err != nil {
		return err
	}
	if len(p.Reason) > maxReasonBytes {
		return fmt.Errorf("pending approval reason exceeds %d bytes", maxReasonBytes)
	}
	if p.Behavior == BehaviorRequire {
		if strings.TrimSpace(p.ApprovalID) == "" {
			return fmt.Errorf("required approval id is missing")
		}
	} else if p.ApprovalID != "" {
		return fmt.Errorf("only required calls may carry an approval id")
	}
	return nil
}

// Suspension is the serializable execution plan for one model-produced Tool
// batch. At least one Call must require approval.
type Suspension struct {
	ID        string        `json:"id"`
	TurnStart int           `json:"turn_start"`
	Calls     []PendingCall `json:"calls"`
}

func (s Suspension) Clone() Suspension {
	s.Calls = append([]PendingCall(nil), s.Calls...)
	return s
}

func (s Suspension) Validate() error {
	if strings.TrimSpace(s.ID) == "" || s.TurnStart < 0 || len(s.Calls) == 0 {
		return fmt.Errorf("suspension id, turn start, and calls are required")
	}
	callIDs := make(map[string]struct{}, len(s.Calls))
	approvalIDs := make(map[string]struct{})
	required := 0
	for index, call := range s.Calls {
		if err := call.Validate(); err != nil {
			return fmt.Errorf("call %d: %w", index, err)
		}
		if _, exists := callIDs[call.CallID]; exists {
			return fmt.Errorf("duplicate pending call id %q", call.CallID)
		}
		callIDs[call.CallID] = struct{}{}
		if call.Behavior == BehaviorRequire {
			required++
			if _, exists := approvalIDs[call.ApprovalID]; exists {
				return fmt.Errorf("duplicate approval id %q", call.ApprovalID)
			}
			approvalIDs[call.ApprovalID] = struct{}{}
		}
	}
	if required == 0 {
		return fmt.Errorf("suspension has no required approval")
	}
	return nil
}

type ResolutionStatus string

const (
	ResolutionApproved ResolutionStatus = "approved"
	ResolutionRejected ResolutionStatus = "rejected"
)

// Resolution is supplied by the embedding application to Agent.Resume.
type Resolution struct {
	ApprovalID string           `json:"approval_id"`
	Status     ResolutionStatus `json:"status"`
	Reason     string           `json:"reason,omitempty"`
}

func (r Resolution) Validate() error {
	if strings.TrimSpace(r.ApprovalID) == "" {
		return fmt.Errorf("approval id is required")
	}
	if r.Status != ResolutionApproved && r.Status != ResolutionRejected {
		return fmt.Errorf("unsupported approval resolution %q", r.Status)
	}
	if len(r.Reason) > maxReasonBytes {
		return fmt.Errorf("approval resolution reason exceeds %d bytes", maxReasonBytes)
	}
	return nil
}

// ArgumentsDigest binds the model-visible Tool name and exact JSON bytes.
func ArgumentsDigest(tool string, arguments json.RawMessage) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(tool))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(arguments)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func ValidDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func IsPolicyFailure(err error) bool {
	var value *Error
	return errors.As(err, &value) && value != nil && value.Code == ErrorPolicyFailed
}
