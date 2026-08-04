// Package permission 定义工具的进程内授权决策。
//
// Policy 只看到 Tool/Action/Target 元数据，不得携带凭据或完整命令参数。
package permission

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
)

// Action 表示工具请求的效果类别。
type Action string

const (
	ActionRead    Action = "read"
	ActionWrite   Action = "write"
	ActionEdit    Action = "edit"
	ActionSearch  Action = "search"
	ActionExecute Action = "execute"
)

// Validate 拒绝不在 Core 固定词表中的 Action。
func (a Action) Validate() error {
	switch a {
	case ActionRead, ActionWrite, ActionEdit, ActionSearch, ActionExecute:
		return nil
	default:
		return fmt.Errorf("unsupported permission action %q", a)
	}
}

// Request 仅含授权决策所需元数据。
// Target 标识路径或工作目录，不得含凭据或完整命令参数；空 Target 表示工具级授权。
type Request struct {
	Tool   string
	Action Action
	Target string
}

func (r Request) Validate() error {
	if r.Tool == "" || strings.TrimSpace(r.Tool) != r.Tool {
		return fmt.Errorf("permission tool name is required without surrounding whitespace")
	}
	if len(r.Tool) > 128 {
		return fmt.Errorf("permission tool name exceeds 128 bytes")
	}
	if err := r.Action.Validate(); err != nil {
		return err
	}
	if len(r.Target) > 4096 {
		return fmt.Errorf("permission target exceeds 4096 bytes")
	}
	if strings.IndexByte(r.Target, 0) >= 0 {
		return fmt.Errorf("permission target contains a null byte")
	}
	return nil
}

// Decision 是一次策略评估的完整结果。
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

// Validate 只允许 allow / deny。
func (d Decision) Validate() error {
	switch d {
	case DecisionAllow, DecisionDeny:
		return nil
	default:
		return fmt.Errorf("unsupported permission decision %q", d)
	}
}

// Policy 做出一次有界授权决策；被并发工具共享时必须并发安全。
type Policy interface {
	Decide(context.Context, Request) (Decision, error)
}

// PolicyFunc 将函数适配为 Policy。
type PolicyFunc func(context.Context, Request) (Decision, error)

func (f PolicyFunc) Decide(ctx context.Context, request Request) (Decision, error) {
	if f == nil {
		return "", fmt.Errorf("permission policy function is nil")
	}
	return f(ctx, request)
}

// AllowAll 放行所有合法请求；应用必须显式选择启用。
type AllowAll struct{}

func (AllowAll) Decide(context.Context, Request) (Decision, error) {
	return DecisionAllow, nil
}

// DenyAll rejects every request.
type DenyAll struct{}

func (DenyAll) Decide(context.Context, Request) (Decision, error) {
	return DecisionDeny, nil
}

// ActionPolicy permits only the actions supplied to NewActionPolicy. It is
// immutable after construction and safe for concurrent use.
type ActionPolicy struct {
	allowed map[Action]struct{}
}

// NewActionPolicy copies and validates an allowlist of actions. An empty list
// creates a deny-all policy.
func NewActionPolicy(actions ...Action) (*ActionPolicy, error) {
	allowed := make(map[Action]struct{}, len(actions))
	for _, action := range actions {
		if err := action.Validate(); err != nil {
			return nil, err
		}
		allowed[action] = struct{}{}
	}
	return &ActionPolicy{allowed: allowed}, nil
}

func (p *ActionPolicy) Decide(_ context.Context, request Request) (Decision, error) {
	if p == nil {
		return DecisionDeny, nil
	}
	// The map is constructed once and never mutated.
	if _, allowed := p.allowed[request.Action]; allowed {
		return DecisionAllow, nil
	}
	return DecisionDeny, nil
}

// Actions returns a sorted copy of the allowed actions.
func (p *ActionPolicy) Actions() []Action {
	if p == nil {
		return nil
	}
	actions := make([]Action, 0, len(p.allowed))
	for action := range p.allowed {
		actions = append(actions, action)
	}
	sort.Slice(actions, func(first, second int) bool { return actions[first] < actions[second] })
	return actions
}

// ErrorCode distinguishes an ordinary denial from invalid Core input and a
// failed policy extension.
type ErrorCode string

const (
	ErrorDenied         ErrorCode = "permission_denied"
	ErrorInvalidRequest ErrorCode = "invalid_permission_request"
	ErrorPolicyFailed   ErrorCode = "permission_policy_failed"
)

// Error is returned by Check for non-cancellation failures. Error does not
// include Target in its message, although the original Request remains
// available for structured diagnostics.
type Error struct {
	Code    ErrorCode
	Request Request
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return "permission check failed"
	}
	switch e.Code {
	case ErrorDenied:
		return fmt.Sprintf("permission denied for %s action by tool %q", e.Request.Action, e.Request.Tool)
	case ErrorInvalidRequest:
		return "permission request is invalid"
	case ErrorPolicyFailed:
		return "permission policy failed"
	default:
		return "permission check failed"
	}
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsDenied reports whether err contains an ordinary permission denial.
func IsDenied(err error) bool {
	var value *Error
	return errors.As(err, &value) && value != nil && value.Code == ErrorDenied
}

// Check validates a request and invokes policy with panic containment. A nil
// policy denies the request. Cancellation is returned directly so callers do
// not misclassify it as a permission denial or policy failure.
func Check(ctx context.Context, policy Policy, request Request) (err error) {
	if ctx == nil {
		return &Error{Code: ErrorInvalidRequest, Request: request, Cause: fmt.Errorf("context is nil")}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return &Error{Code: ErrorInvalidRequest, Request: request, Cause: err}
	}
	if policy == nil {
		return &Error{Code: ErrorDenied, Request: request}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &Error{
				Code:    ErrorPolicyFailed,
				Request: request,
				Cause:   fmt.Errorf("permission policy panic: %v\n%s", recovered, debug.Stack()),
			}
		}
	}()
	decision, policyErr := policy.Decide(ctx, request)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if policyErr != nil {
		return &Error{Code: ErrorPolicyFailed, Request: request, Cause: policyErr}
	}
	if err := decision.Validate(); err != nil {
		return &Error{Code: ErrorPolicyFailed, Request: request, Cause: err}
	}
	if decision == DecisionDeny {
		return &Error{Code: ErrorDenied, Request: request}
	}
	return nil
}
