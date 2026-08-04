// Package tool defines native tool execution contracts.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

type Tool interface {
	Definition() model.ToolDefinition
	Execute(context.Context, json.RawMessage) (Result, error)
}

type Result struct {
	Content []message.Content `json:"content"`
	State   json.RawMessage   `json:"state,omitempty"`
}

func TextResult(text string) Result {
	return Result{Content: []message.Content{{Type: message.ContentText, Text: text}}}
}

// Error is safe to return to the model as a Tool Result.
type Error struct {
	Code        string
	SafeMessage string
	Retryable   bool
	Cause       error
}

func NewError(code, safeMessage string, retryable bool, cause error) *Error {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "tool_failed"
	}
	safeMessage = strings.TrimSpace(safeMessage)
	if safeMessage == "" {
		safeMessage = "The tool failed. Retry or use another approach."
	}
	return &Error{Code: code, SafeMessage: safeMessage, Retryable: retryable, Cause: cause}
}

func (e *Error) Error() string {
	if e == nil || strings.TrimSpace(e.SafeMessage) == "" {
		return "the tool failed"
	}
	return e.SafeMessage
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// RuntimeError is not safe to expose to the model and terminates the run.
type RuntimeError struct {
	Code  string
	Cause error
}

func (e *RuntimeError) Error() string {
	if e == nil || e.Cause == nil {
		return "tool runtime failed"
	}
	return fmt.Sprintf("tool runtime failed (%s): %v", e.Code, e.Cause)
}

func (e *RuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
