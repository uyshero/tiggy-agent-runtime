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
// 主模型与每个 Fallback 各是一个 Target；降级后用该 Target 自己的 Planner。
type ModelTarget struct {
	Model             model.Model
	Identity          model.Identity
	ContextPlanner    contextmanager.Planner
	ContextCompressor contextmanager.Compressor
}

// ModelFailureAction 是 FailurePolicy 对一次模型失败的处置。
type ModelFailureAction string

const (
	ModelFailureFail     ModelFailureAction = "fail"     // 终止，把错误交回 Run
	ModelFailureRetry    ModelFailureAction = "retry"    // 同一目标再试（可带 Delay）
	ModelFailureFallback ModelFailureAction = "fallback" // 切到下一个 Target
	// ModelFailureCompressRetry structurally compresses the failed request view
	// before one same-target retry. Runtime permits it only for context length.
	ModelFailureCompressRetry ModelFailureAction = "compress_retry"
)

// ModelFailure 汇总当前目标、尝试次数与供应商错误，供策略决策。
type ModelFailure struct {
	TargetIndex              int                  // 当前目标在 targets 中的下标
	TargetCount              int                  // 目标总数（主 + Fallbacks）
	Attempt                  int                  // 当前目标上的第几次尝试
	RemainingCalls           int                  // MaxModelCalls 还剩几次
	Error                    *model.ProviderError // 可为 nil（非结构化失败）
	CanCompress              bool                 // 当前 Target 同时配置 Planner 和 Compressor
	ContextRecoveryAttempted bool                 // 当前 Target 已用过一次压缩恢复
}

// ModelFailureDecision 是策略输出的动作、延迟与原因。
type ModelFailureDecision struct {
	Action ModelFailureAction
	Delay  time.Duration // 仅 Retry 有意义
	Reason string        // 写入 RetryScheduled / FallbackActivated 事件
}

// ModelFailurePolicy 决定模型失败后是失败、重试还是切换 Fallback。
type ModelFailurePolicy interface {
	Decide(ModelFailure) ModelFailureDecision
}

// TransientModelFailurePolicy 仅重试供应商明确标记为可重试的失败；
// 当前目标尝试耗尽后可切换到已配置的 Fallback 模型。
type TransientModelFailurePolicy struct {
	MaxAttemptsPerTarget  int           // 每个目标最多尝试次数；<=0 视为 1
	BaseDelay             time.Duration // 指数退避基数
	MaxDelay              time.Duration // 退避上限；也钳制 RetryAfter
	EnableFallback        bool          // 是否允许切到下一个 Target
	EnableContextRecovery bool          // 是否允许 Context Length 失败触发一次压缩重试
}

func (p TransientModelFailurePolicy) Decide(failure ModelFailure) ModelFailureDecision {
	if p.EnableContextRecovery && failure.Error != nil && failure.Error.Class == model.ErrorContextLength &&
		failure.CanCompress && !failure.ContextRecoveryAttempted && failure.RemainingCalls > 0 {
		return ModelFailureDecision{Action: ModelFailureCompressRetry, Reason: "context_length_recovery"}
	}
	// 非 ProviderError、不可重试、或没有剩余调用次数 → 直接失败。
	if failure.Error == nil || !failure.Error.Retryable || failure.RemainingCalls <= 0 {
		return ModelFailureDecision{Action: ModelFailureFail, Reason: "not_retryable"}
	}
	maxAttempts := p.MaxAttemptsPerTarget
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if failure.Attempt < maxAttempts {
		// 指数退避，并尊重供应商的 RetryAfter。
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
	// 本目标次数用尽：若还有下一个 Target 且允许降级，则 Fallback。
	if p.EnableFallback && failure.TargetIndex+1 < failure.TargetCount {
		return ModelFailureDecision{Action: ModelFailureFallback, Reason: "target_attempts_exhausted"}
	}
	return ModelFailureDecision{Action: ModelFailureFail, Reason: "attempts_exhausted"}
}

// failFastModelPolicy 是 New 在未配置 Policy 时的默认：任何失败立即终止。
type failFastModelPolicy struct{}

func (failFastModelPolicy) Decide(ModelFailure) ModelFailureDecision {
	return ModelFailureDecision{Action: ModelFailureFail, Reason: "fail_fast"}
}

// decideModelFailure 调用策略并校验返回值；隔离 panic，拒绝非法 Action/负 Delay。
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
	case ModelFailureCompressRetry:
		if failure.Error == nil || failure.Error.Class != model.ErrorContextLength || !failure.CanCompress || failure.ContextRecoveryAttempted {
			return ModelFailureDecision{}, fmt.Errorf("model failure policy selected unavailable context recovery")
		}
		if decision.Delay != 0 {
			return ModelFailureDecision{}, fmt.Errorf("context recovery decision cannot specify a delay")
		}
		return decision, nil
	case ModelFailureFail, ModelFailureRetry, ModelFailureFallback:
		return decision, nil
	default:
		return ModelFailureDecision{}, fmt.Errorf("model failure policy returned unsupported action %q", decision.Action)
	}
}

// retryDelay 按失败次数做指数退避：base, 2*base, 4*base...，并钳制在 maximum。
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
