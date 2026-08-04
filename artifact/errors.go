package artifact

import "fmt"

// ErrorCode 对 Artifact 卸载/存储失败做稳定分类。
type ErrorCode string

const (
	ErrorInvalidConfig   ErrorCode = "invalid_config"
	ErrorInvalidRequest  ErrorCode = "invalid_request"
	ErrorPayloadLimit    ErrorCode = "payload_limit"
	ErrorStoreFailed     ErrorCode = "store_failed"
	ErrorInvalidResponse ErrorCode = "invalid_response"
	ErrorInvalidDecision ErrorCode = "invalid_decision"
)

// Error 保留稳定 Code 与底层 Cause。
type Error struct {
	Code  ErrorCode
	Cause error
}

func (e *Error) Error() string {
	if e == nil || e.Cause == nil {
		return "artifact operation failed"
	}
	return fmt.Sprintf("artifact operation failed (%s): %v", e.Code, e.Cause)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
