// Package tool 定义原生工具执行契约。
//
// Error 可安全回传给模型；RuntimeError 不可暴露给模型，会终止 Run。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// Tool 是可注册到 Toolkit 的原生工具实现。
type Tool interface {
	Definition() model.ToolDefinition
	Execute(context.Context, json.RawMessage) (Result, error)
}

// Result 是工具执行成功后返回给模型的内容与可选状态。
type Result struct {
	Content []message.Content `json:"content"`
	State   json.RawMessage   `json:"state,omitempty"`
}

// TextResult 构造仅含一段文本 Content 的结果。
func TextResult(text string) Result {
	return Result{Content: []message.Content{{Type: message.ContentText, Text: text}}}
}

// Error 可安全作为 Tool Result 回传给模型（含可重试提示）。
type Error struct {
	Code        string
	SafeMessage string
	Retryable   bool
	Cause       error
}

// NewError 构造模型可见错误；空 code/message 会填入默认值。
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

// RuntimeError 不可暴露给模型，会导致 Run 终止。
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
