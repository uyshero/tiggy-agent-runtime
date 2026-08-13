package orchestrator

import (
	"errors"
	"fmt"
	"strings"
)

type ErrorClass string

const (
	ErrorInvalidBrief ErrorClass = "invalid_brief"
	ErrorFactory      ErrorClass = "factory"
	ErrorExecution    ErrorClass = "execution"
	ErrorArtifact     ErrorClass = "artifact"
	ErrorCheckpoint   ErrorClass = "checkpoint"
	ErrorVerification ErrorClass = "verification"
	ErrorEventSink    ErrorClass = "event_sink"
	ErrorCanceled     ErrorClass = "canceled"
	ErrorInternal     ErrorClass = "internal"
)

type RunError struct {
	Class       ErrorClass
	Code        string
	SafeMessage string
	Cause       error
}

func (e *RunError) Error() string {
	if e == nil {
		return "orchestrator failed"
	}
	message := strings.TrimSpace(e.SafeMessage)
	if message == "" {
		message = "The orchestration failed."
	}
	if e.Code != "" {
		return fmt.Sprintf("orchestrator failed (%s/%s): %s", e.Class, e.Code, message)
	}
	return fmt.Sprintf("orchestrator failed (%s): %s", e.Class, message)
}

func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type ErrorPayload struct {
	Class   ErrorClass `json:"class"`
	Code    string     `json:"code"`
	Message string     `json:"message"`
}

func errorPayload(err *RunError) *ErrorPayload {
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(err.SafeMessage)
	if message == "" {
		message = "The orchestration failed."
	}
	return &ErrorPayload{Class: err.Class, Code: err.Code, Message: message}
}

func canceledError(cause error) *RunError {
	if cause == nil {
		cause = errors.New("execution canceled")
	}
	return &RunError{Class: ErrorCanceled, Code: "context_canceled", SafeMessage: "The orchestration was canceled.", Cause: cause}
}

func sinkError(cause error) *RunError {
	return &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The orchestration event consumer failed.", Cause: cause}
}
