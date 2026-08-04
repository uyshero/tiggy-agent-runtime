package agent

import (
	"fmt"
	"strings"
)

// ErrorClass 对 Run 失败做稳定分类，便于观测与上层映射。
type ErrorClass string

const (
	ErrorInvalidInput ErrorClass = "invalid_input"
	ErrorModel        ErrorClass = "model"
	ErrorToolRuntime  ErrorClass = "tool_runtime"
	ErrorApproval     ErrorClass = "approval"
	ErrorArtifact     ErrorClass = "artifact"
	ErrorMemory       ErrorClass = "memory"
	ErrorLimit        ErrorClass = "limit"
	ErrorEventSink    ErrorClass = "event_sink"
	ErrorCanceled     ErrorClass = "canceled"
	ErrorInternal     ErrorClass = "internal"
)

// RunError 是一次 Agent Run 的终态错误；SafeMessage 可对模型/用户展示。
type RunError struct {
	Class       ErrorClass
	Code        string
	SafeMessage string
	Retryable   bool
	Cause       error
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

func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
