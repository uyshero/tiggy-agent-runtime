// Package command defines bounded shell-command execution contracts.
package command

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// Request is a shell script and its canonical Root-relative working directory.
type Request struct {
	Script    string
	Directory string
}

// Validate checks the portable request contract. Resource limits belong to
// the Runner implementation and the calling Tool.
func (r Request) Validate() error {
	if r.Script == "" {
		return fmt.Errorf("command script is required")
	}
	if strings.IndexByte(r.Script, 0) >= 0 {
		return fmt.Errorf("command script contains a null byte")
	}
	if r.Directory == "" {
		return fmt.Errorf("command directory is required")
	}
	if strings.IndexByte(r.Directory, 0) >= 0 || strings.ContainsRune(r.Directory, '\\') {
		return fmt.Errorf("command directory is not a canonical slash-separated path")
	}
	cleaned := path.Clean(r.Directory)
	if cleaned != r.Directory || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("command directory must be a canonical relative path")
	}
	return nil
}

// Result contains owned output buffers. ExitCode is -1 when the shell was
// terminated without a portable numeric exit code.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Clone returns a deep copy safe to retain across asynchronous boundaries.
func (r Result) Clone() Result {
	r.Stdout = append([]byte(nil), r.Stdout...)
	r.Stderr = append([]byte(nil), r.Stderr...)
	return r
}

// Validate rejects impossible successful Runner results.
func (r Result) Validate() error {
	if r.ExitCode < -1 {
		return fmt.Errorf("command exit code cannot be less than -1")
	}
	return nil
}

// Runner executes shell requests. Implementations shared across calls must be
// concurrency-safe, honor Context cancellation, and bound captured output.
// A non-zero process exit is a Result, not an error.
type Runner interface {
	Run(context.Context, Request) (Result, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, Request) (Result, error)

func (f RunnerFunc) Run(ctx context.Context, request Request) (Result, error) {
	if f == nil {
		return Result{}, fmt.Errorf("command runner function is nil")
	}
	return f(ctx, request)
}

// ErrorCode classifies command infrastructure failures.
type ErrorCode string

const (
	ErrorInvalidRequest   ErrorCode = "invalid_command_request"
	ErrorInvalidDirectory ErrorCode = "invalid_command_directory"
	ErrorStartFailed      ErrorCode = "command_start_failed"
	ErrorOutputLimit      ErrorCode = "command_output_limit"
	ErrorRunnerFailed     ErrorCode = "command_runner_failed"
)

// Error preserves the infrastructure cause without exposing it to the model.
type Error struct {
	Code  ErrorCode
	Cause error
}

func (e *Error) Error() string {
	if e == nil {
		return "command execution failed"
	}
	return fmt.Sprintf("command execution failed (%s)", e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
