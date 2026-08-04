package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const (
	// WriteToolName 是 WriteTool 对模型可见的稳定名称。
	WriteToolName = "builtin.write"
	// DefaultMaxWriteBytes 默认限制模型写入内容大小（1 MiB）。
	DefaultMaxWriteBytes int64 = 1 << 20
)

// WriteOptions 配置 WriteTool；Permission 为 nil 时拒绝所有调用。
type WriteOptions struct {
	FileSystem safespace.FileSystem
	Permission permission.Policy
	MaxBytes   int64
}

// WriteTool 通过 FileSystem 创建或覆盖有界 UTF-8 文件。
type WriteTool struct {
	fileSystem safespace.FileSystem
	permission permission.Policy
	maxBytes   int64
}

var _ tool.Tool = (*WriteTool)(nil)

// NewWrite 构造有界文本文件写入工具。
func NewWrite(options WriteOptions) (*WriteTool, error) {
	if options.FileSystem == nil {
		return nil, fmt.Errorf("write tool filesystem is required")
	}
	maximum := options.MaxBytes
	if maximum == 0 {
		maximum = DefaultMaxWriteBytes
	}
	if maximum < 0 {
		return nil, fmt.Errorf("write tool maximum bytes must be positive")
	}
	return &WriteTool{fileSystem: options.FileSystem, permission: options.Permission, maxBytes: maximum}, nil
}

func (w *WriteTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:             WriteToolName,
		Description:      "Create or replace a UTF-8 text file in the configured file space.",
		ConcurrencyClass: model.ToolConcurrencySequential,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","minLength":1,"maxLength":4096},
				"content":{"type":"string"}
			},
			"required":["path","content"],
			"additionalProperties":false
		}`),
	}
}

func (w *WriteTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return tool.Result{}, err
	}
	path, err := normalizedFilePath(input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	if int64(len(input.Content)) > w.maxBytes {
		return tool.Result{}, tool.NewError("file_too_large", "The content exceeds the configured write limit.", false, nil)
	}
	if err := authorize(ctx, w.permission, WriteToolName, permission.ActionWrite, path); err != nil {
		return tool.Result{}, err
	}
	if err := w.fileSystem.WriteFile(ctx, path, []byte(input.Content), 0o644); err != nil {
		return tool.Result{}, fileSystemError("write", err)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(fmt.Sprintf("Wrote %d bytes to %q.", len(input.Content), path)), nil
}
