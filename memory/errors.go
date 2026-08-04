package memory

import "fmt"

// ErrorCode 对 Memory 契约内的失败做稳定分类，便于上层映射 FailureMode。
type ErrorCode string

const (
	ErrorInvalidConfig  ErrorCode = "invalid_config"   // 配置非法
	ErrorInvalidRequest ErrorCode = "invalid_request"  // 请求非法
	ErrorInvalidResult  ErrorCode = "invalid_result"   // 实现返回结果非法
	ErrorLimit          ErrorCode = "limit"            // 容量/字节上限
	ErrorAdmission      ErrorCode = "admission_failed" // Admission 决策失败
	ErrorConflict       ErrorCode = "conflict"         // 同 Scope 下 ID 冲突
	ErrorPanic          ErrorCode = "panic"            // 回调 panic 已被隔离
)

// Error 保留稳定的契约 Code 与底层 Cause。
type Error struct {
	Code  ErrorCode
	Cause error
}

func (e *Error) Error() string {
	if e == nil || e.Cause == nil {
		return "memory operation failed"
	}
	return fmt.Sprintf("memory operation failed (%s): %v", e.Code, e.Cause)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
