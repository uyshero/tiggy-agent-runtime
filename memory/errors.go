package memory

import "fmt"

// ErrorCode classifies validation failures inside the Memory contract.
type ErrorCode string

const (
	ErrorInvalidConfig  ErrorCode = "invalid_config"
	ErrorInvalidRequest ErrorCode = "invalid_request"
	ErrorInvalidResult  ErrorCode = "invalid_result"
	ErrorLimit          ErrorCode = "limit"
	ErrorAdmission      ErrorCode = "admission_failed"
	ErrorConflict       ErrorCode = "conflict"
	ErrorPanic          ErrorCode = "panic"
)

// Error preserves the stable contract code and its underlying cause.
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
