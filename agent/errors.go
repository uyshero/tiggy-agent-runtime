package agent

import (
	"fmt"
	"strings"
)

// ErrorClass 对 Run 失败做稳定分类，便于观测与上层映射。
// 会出现在 RunError.Class 和 event.ErrorPayload.Class 中。
type ErrorClass string

const (
	ErrorInvalidInput ErrorClass = "invalid_input" // 调用方输入/State 不合法
	ErrorModel        ErrorClass = "model"         // 模型/供应商失败或非法响应
	ErrorToolRuntime  ErrorClass = "tool_runtime"  // 工具 RuntimeError（不可回传模型）
	ErrorApproval     ErrorClass = "approval"      // 审批策略自身失败
	ErrorArtifact     ErrorClass = "artifact"      // 大结果卸载失败
	ErrorMemory       ErrorClass = "memory"        // 回忆/观察失败（非 degrade）
	ErrorLimit        ErrorClass = "limit"         // 迭代/Token/工具次数等上限
	ErrorEventSink    ErrorClass = "event_sink"    // 事件消费者失败
	ErrorTransition   ErrorClass = "transition"    // 外部 Transition Hook 持久化失败
	ErrorCanceled     ErrorClass = "canceled"      // Context 取消或超时
	ErrorInternal     ErrorClass = "internal"      // Runtime 内部不变量破坏
)

// RunError 是一次 Agent Run 的终态错误。
//
// SafeMessage / Class / Code 可对模型或用户展示；
// Cause 仅供日志与排障，不会进入事件 Payload。
type RunError struct {
	Class       ErrorClass // 稳定大类
	Code        string     // 更细的机器可读码，如 max_iterations
	SafeMessage string     // 安全可读说明
	Retryable   bool       // 提示上层/模型是否值得重试
	Cause       error      // 底层原因（Unwrap）
}

func (e *RunError) Error() string {
	if e == nil {
		return "agent run failed"
	}
	message := strings.TrimSpace(e.SafeMessage)
	if message == "" {
		message = "agent run failed"
	}
	if e.Code != "" {
		return fmt.Sprintf("agent run failed (%s/%s): %s", e.Class, e.Code, message)
	}
	return fmt.Sprintf("agent run failed (%s): %s", e.Class, message)
}

// Unwrap 支持 errors.Is / errors.As 链到 Cause。
func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
