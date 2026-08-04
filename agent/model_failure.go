package agent

import (
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// ModelTarget 描述一个可调用的模型绑定及其专用上下文规划/压缩器。
type ModelTarget struct {
	Model             model.Model
	Identity          model.Identity
	ContextPlanner    contextmanager.Planner
	ContextCompressor contextmanager.Compressor
}

// ModelFailureAction 是 FailurePolicy 对一次模型失败的处置。
type ModelFailureAction string

const (
	ModelFailureFail     ModelFailureAction = "fail"
	ModelFailureRetry    ModelFailureAction = "retry"
	ModelFailureFallback ModelFailureAction = "fallback"
)

// ModelFailure 汇总当前目标、尝试次数与供应商错误，供策略决策。
type ModelFailure struct {
	TargetIndex    int
	TargetCount    int
	Attempt        int
	RemainingCalls int
	Error          *model.ProviderError
}

// ModelFailureDecision 是策略输出的动作、延迟与原因。
type ModelFailureDecision struct {
	Action ModelFailureAction
	Delay  time.Duration
	Reason string
}

// ModelFailurePolicy 决定模型失败后是失败、重试还是切换 Fallback。
type ModelFailurePolicy interface {
	Decide(ModelFailure) ModelFailureDecision
}

// TransientModelFailurePolicy 仅重试供应商明确标记为可重试的失败；
// 当前目标尝试耗尽后可切换到已配置的 Fallback 模型。
type TransientModelFailurePolicy struct {
	MaxAttemptsPerTarget int
	BaseDelay            time.Duration
	MaxDelay             time.Duration
	EnableFallback       bool
}

func (p TransientModelFailurePolicy) Decide(failure ModelFailure) ModelFailureDecision {
	if failure.Error == nil || !failure.Error.Retryable || failure.RemainingCalls <= 0 {
		return ModelFailureDecision{Action: ModelFailureFail, Reason: "not_retryable"}
	}
	maxAttempts := p.MaxAttemptsPerTarget
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if failure.Attempt < maxAttempts {
		delay := retryDelay(p.BaseDelay, p.MaxDelay, failure.Attempt)
		if failure.Error.RetryAfter > delay {
			delay = failure.Error.RetryAfter
		}
		if p.MaxDelay > 0 && delay > p.MaxDelay {
			delay = p.MaxDelay
		}
		return ModelFailureDecision{
			Action: ModelFailureRetry,
			Delay:  delay,
			Reason: "retryable_provider_error",
		}
	}
	if p.EnableFallback && failure.TargetIndex+1 < failure.TargetCount {
		return ModelFailureDecision{Action: ModelFailureFallback, Reason: "target_attempts_exhausted"}
	}
	return ModelFailureDecision{Action: ModelFailureFail, Reason: "attempts_exhausted"}
}

type failFastModelPolicy struct{}

func (failFastModelPolicy) Decide(ModelFailure) ModelFailureDecision {
	return ModelFailureDecision{Action: ModelFailureFail, Reason: "fail_fast"}
}

func decideModelFailure(policy ModelFailurePolicy, failure ModelFailure) (decision ModelFailureDecision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("model failure policy panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	decision = policy.Decide(failure)
	decision.Reason = strings.TrimSpace(decision.Reason)
	if decision.Reason == "" {
		decision.Reason = string(decision.Action)
	}
	if decision.Delay < 0 {
		return ModelFailureDecision{}, fmt.Errorf("model failure policy returned a negative delay")
	}
	switch decision.Action {
	case ModelFailureFail, ModelFailureRetry, ModelFailureFallback:
		return decision, nil
	default:
		return ModelFailureDecision{}, fmt.Errorf("model failure policy returned unsupported action %q", decision.Action)
	}
}

func retryDelay(base, maximum time.Duration, failedAttempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	delay := base
	for current := 1; current < failedAttempt; current++ {
		if maximum > 0 && delay >= maximum {
			return maximum
		}
		if delay > time.Duration(1<<63-1)/2 {
			if maximum > 0 {
				return maximum
			}
			return time.Duration(1<<63 - 1)
		}
		delay *= 2
	}
	if maximum > 0 && delay > maximum {
		return maximum
	}
	return delay
}
