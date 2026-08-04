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
	// ReadToolName is the stable model-visible name of ReadTool.
	ReadToolName = "builtin.read"
	// DefaultMaxReadBytes bounds file content returned to the model by default.
	DefaultMaxReadBytes int64 = 128 << 10
)

// ReadOptions configures a ReadTool. A nil Permission denies every call.
type ReadOptions struct {
	FileSystem safespace.FileSystem
	Permission permission.Policy
	MaxBytes   int64
}

// ReadTool returns bounded UTF-8 file content from a FileSystem.
type ReadTool struct {
	fileSystem safespace.FileSystem
	permission permission.Policy
	maxBytes   int64
}

var _ tool.Tool = (*ReadTool)(nil)

// NewRead constructs a bounded text-file reader.
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
	summary := fmt.Sprintf("Read %d bytes from %q.", len(data), path)
	contents := []message.Content{{Type: message.ContentText, Text: summary}}
	if len(data) == 0 {
		contents[0].Text = fmt.Sprintf("Read 0 bytes from %q; the file is empty.", path)
	} else {
		contents = append(contents, message.Content{Type: message.ContentText, Text: string(data)})
	}
	return tool.Result{Content: contents}, nil
}
