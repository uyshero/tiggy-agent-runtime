package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

// remoteTool 把远程 MCP 工具适配为本地 tool.Tool；Definition 已是 Namespace 限定名。
type remoteTool struct {
	client     *Client
	definition model.ToolDefinition
	remoteName string
}

var _ tool.Tool = (*remoteTool)(nil)

func (r *remoteTool) Definition() model.ToolDefinition {
	return r.definition.Clone()
}

func (r *remoteTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	callCtx, cancel := context.WithTimeout(ctx, r.client.limits.ToolTimeout)
	defer cancel()
	result, err := r.client.call(callCtx, r.remoteName, arguments)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return tool.Result{}, ctxErr
	}
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return tool.Result{}, tool.NewError("mcp_tool_timeout", "The MCP tool exceeded its execution timeout.", false, err)
	}
	if err != nil {
		var mcpError *Error
		if errors.As(err, &mcpError) && mcpError != nil {
			switch mcpError.Code {
			case ErrorArgumentLimit:
				return tool.Result{}, tool.NewError("mcp_argument_limit", "The MCP tool arguments exceed the configured size limit.", false, err)
			case ErrorOutputLimit:
				return tool.Result{}, tool.NewError("mcp_output_limit", "The MCP tool result exceeded the configured output limit.", false, err)
			case ErrorUnsupportedContent:
				return tool.Result{}, tool.NewError("unsupported_mcp_content", "The MCP tool returned content this runtime cannot represent.", false, err)
			}
			return tool.Result{}, &tool.RuntimeError{Code: string(mcpError.Code), Cause: err}
		}
		return tool.Result{}, &tool.RuntimeError{Code: string(ErrorCallFailed), Cause: err}
	}
	content := message.CloneContent(result.Content)
	if len(content) == 0 && len(result.StructuredContent) > 0 {
		content = []message.Content{{Type: message.ContentText, Text: string(result.StructuredContent)}}
	}
	if len(content) == 0 {
		content = []message.Content{{Type: message.ContentText, Text: "MCP tool completed without content."}}
	}
	validation := message.ToolResult{CallID: "mcp_call", Name: r.remoteName, Content: content, State: result.StructuredContent}
	if err := validation.Validate(); err != nil {
		return tool.Result{}, &tool.RuntimeError{Code: string(ErrorInvalidResponse), Cause: err}
	}
	if result.IsError {
		return tool.Result{}, tool.NewError("mcp_tool_error", remoteErrorMessage(content), false, nil)
	}
	return tool.Result{Content: content, State: append(json.RawMessage(nil), result.StructuredContent...)}, nil
}

func remoteErrorMessage(content []message.Content) string {
	var text []string
	for _, value := range content {
		if value.Type == message.ContentText && strings.TrimSpace(value.Text) != "" {
			text = append(text, value.Text)
		}
	}
	if len(text) == 0 {
		return "The MCP tool reported an error."
	}
	return fmt.Sprintf("The MCP tool reported an error: %s", strings.Join(text, "\n"))
}
