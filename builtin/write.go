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
	// WriteToolName is the stable model-visible name of WriteTool.
	WriteToolName = "builtin.write"
	// DefaultMaxWriteBytes bounds file content accepted from the model by default.
	DefaultMaxWriteBytes int64 = 1 << 20
)

// WriteOptions configures a WriteTool. A nil Permission denies every call.
type WriteOptions struct {
	FileSystem safespace.FileSystem
	Permission permission.Policy
	MaxBytes   int64
}

// WriteTool creates or replaces a bounded UTF-8 file through a FileSystem.
type WriteTool struct {
	fileSystem safespace.FileSystem
	permission permission.Policy
	maxBytes   int64
}

var _ tool.Tool = (*WriteTool)(nil)

// NewWrite constructs a bounded text-file writer.
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
