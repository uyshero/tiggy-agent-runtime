package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const (
	// ReadToolName 是 ReadTool 对模型可见的稳定名称。
	ReadToolName = "builtin.read"
	// DefaultMaxReadBytes 默认限制返回给模型的文件内容大小（128 KiB）。
	DefaultMaxReadBytes int64 = 128 << 10
)

// ReadOptions 配置 ReadTool；Permission 为 nil 时拒绝所有调用。
type ReadOptions struct {
	FileSystem safespace.FileSystem
	Permission permission.Policy
	MaxBytes   int64
}

// ReadTool 从 FileSystem 读取有界 UTF-8 文本文件。
type ReadTool struct {
	fileSystem safespace.FileSystem
	permission permission.Policy
	maxBytes   int64
}

var _ tool.Tool = (*ReadTool)(nil)

// NewRead 构造有界文本文件读取工具。
func NewRead(options ReadOptions) (*ReadTool, error) {
	if options.FileSystem == nil {
		return nil, fmt.Errorf("read tool filesystem is required")
	}
	maximum := options.MaxBytes
	if maximum == 0 {
		maximum = DefaultMaxReadBytes
	}
	if maximum < 0 {
		return nil, fmt.Errorf("read tool maximum bytes must be positive")
	}
	return &ReadTool{fileSystem: options.FileSystem, permission: options.Permission, maxBytes: maximum}, nil
}

func (r *ReadTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:             ReadToolName,
		Description:      "Read a UTF-8 text file from the configured file space.",
		ConcurrencyClass: model.ToolConcurrencySequential,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{"path":{"type":"string","minLength":1,"maxLength":4096}},
			"required":["path"],
			"additionalProperties":false
		}`),
	}
}

func (r *ReadTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return tool.Result{}, err
	}
	path, err := normalizedFilePath(input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	if err := authorize(ctx, r.permission, ReadToolName, permission.ActionRead, path); err != nil {
		return tool.Result{}, err
	}
	data, err := r.fileSystem.ReadFile(ctx, path)
	if err != nil {
		return tool.Result{}, fileSystemError("read", err)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if int64(len(data)) > r.maxBytes {
		return tool.Result{}, tool.NewError("file_too_large", "The file is too large to return to the model.", false, nil)
	}
	if !utf8.Valid(data) {
		return tool.Result{}, tool.NewError("unsupported_file_encoding", "The file is not valid UTF-8 text.", false, nil)
	}
	digest := contentDigest(data)
	summary := fmt.Sprintf("Read %d bytes from %q. file_digest=%s.", len(data), path, digest)
	contents := []message.Content{{Type: message.ContentText, Text: summary}}
	if len(data) == 0 {
		contents[0].Text = fmt.Sprintf("Read 0 bytes from %q; the file is empty. file_digest=%s.", path, digest)
	} else {
		contents = append(contents, message.Content{Type: message.ContentText, Text: string(data)})
	}
	return tool.Result{
		Content: contents,
		State: fileStateJSON(struct {
			Path      string `json:"path"`
			SizeBytes int    `json:"size_bytes"`
			Digest    string `json:"digest"`
		}{Path: path, SizeBytes: len(data), Digest: digest}),
	}, nil
}
