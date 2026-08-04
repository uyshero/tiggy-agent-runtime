package contextmanager

import "fmt"

// ErrorCode 对上下文规划/压缩失败做稳定分类。
type ErrorCode string

const (
	ErrorInvalidConfig      ErrorCode = "invalid_config"
	ErrorInvalidRequest     ErrorCode = "invalid_request"
	ErrorCounterFailed      ErrorCode = "counter_failed"
	ErrorInvalidCount       ErrorCode = "invalid_count"
	ErrorImpossibleBudget   ErrorCode = "impossible_budget"
	ErrorInvalidPlan        ErrorCode = "invalid_plan"
	ErrorInvalidCompression ErrorCode = "invalid_compression"
)

// Error 保留稳定 Code 与底层 Cause。
type Error struct {
	Code  ErrorCode
	Cause error
}

func (e *Error) Error() string {
	if e == nil || e.Cause == nil {
		return "context management failed"
	}
	return fmt.Sprintf("context management failed (%s): %v", e.Code, e.Cause)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
