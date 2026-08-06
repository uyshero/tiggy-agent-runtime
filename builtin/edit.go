package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const (
	// EditToolName 是 EditTool 对模型可见的稳定名称。
	EditToolName = "builtin.edit"
	// DefaultMaxEditBytes 同时限制输入片段与编辑后文件大小（默认 1 MiB）。
	DefaultMaxEditBytes int64 = 1 << 20
)

// EditOptions 配置 EditTool；Permission 为 nil 时拒绝所有调用。
type EditOptions struct {
	FileSystem safespace.FileSystem
	Permission permission.Policy
	MaxBytes   int64
}

// EditTool 在已有 UTF-8 文件中做精确文本替换。
type EditTool struct {
	fileSystem safespace.FileSystem
	permission permission.Policy
	maxBytes   int64
}

var _ tool.Tool = (*EditTool)(nil)

// NewEdit 构造有界精确文本编辑工具。
func NewEdit(options EditOptions) (*EditTool, error) {
	if options.FileSystem == nil {
		return nil, fmt.Errorf("edit tool filesystem is required")
	}
	maximum := options.MaxBytes
	if maximum == 0 {
		maximum = DefaultMaxEditBytes
	}
	if maximum < 0 {
		return nil, fmt.Errorf("edit tool maximum bytes must be positive")
	}
	return &EditTool{fileSystem: options.FileSystem, permission: options.Permission, maxBytes: maximum}, nil
}

func (e *EditTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:             EditToolName,
		Description:      "Replace exact text in an existing UTF-8 file.",
		ConcurrencyClass: model.ToolConcurrencySequential,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","minLength":1,"maxLength":4096},
				"old_text":{"type":"string","minLength":1},
				"new_text":{"type":"string"},
				"replace_all":{"type":"boolean"},
				"expected_digest":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},
				"dry_run":{"type":"boolean"}
			},
			"required":["path","old_text","new_text"],
			"additionalProperties":false
		}`),
	}
}

func (e *EditTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var input struct {
		Path       string `json:"path"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		ReplaceAll bool   `json:"replace_all"`
		Expected   string `json:"expected_digest"`
		DryRun     bool   `json:"dry_run"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return tool.Result{}, err
	}
	if err := validateExpectedDigest(input.Expected); err != nil {
		return tool.Result{}, err
	}
	if input.OldText == "" {
		return tool.Result{}, invalidArguments(fmt.Errorf("old_text is required"))
	}
	if int64(len(input.OldText)) > e.maxBytes || int64(len(input.NewText)) > e.maxBytes {
		return tool.Result{}, tool.NewError("file_too_large", "The edit text exceeds the configured size limit.", false, nil)
	}
	path, err := normalizedFilePath(input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	if err := authorize(ctx, e.permission, EditToolName, permission.ActionEdit, path); err != nil {
		return tool.Result{}, err
	}
	data, err := e.fileSystem.ReadFile(ctx, path)
	if err != nil {
		return tool.Result{}, fileSystemError("read", err)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if int64(len(data)) > e.maxBytes {
		return tool.Result{}, tool.NewError("file_too_large", "The file exceeds the configured edit limit.", false, nil)
	}
	if !utf8.Valid(data) {
		return tool.Result{}, tool.NewError("unsupported_file_encoding", "The file is not valid UTF-8 text.", false, nil)
	}
	beforeDigest := contentDigest(data)
	if input.Expected != "" && input.Expected != beforeDigest {
		return tool.Result{}, tool.NewError("file_changed", "The file content digest does not match expected_digest; read the file again before editing.", false, nil)
	}
	content := string(data)
	matches := strings.Count(content, input.OldText)
	if matches == 0 {
		return tool.Result{}, tool.NewError("text_not_found", "The text to replace was not found in the file.", false, nil)
	}
	if matches > 1 && !input.ReplaceAll {
		return tool.Result{}, tool.NewError("ambiguous_match", "The text occurs more than once; provide a unique match or enable replace_all.", false, nil)
	}
	replacementCount := 1
	if input.ReplaceAll {
		replacementCount = matches
	}
	updated := strings.Replace(content, input.OldText, input.NewText, replacementCount)
	if int64(len(updated)) > e.maxBytes {
		return tool.Result{}, tool.NewError("file_too_large", "The edited file would exceed the configured size limit.", false, nil)
	}
	afterDigest := contentDigest([]byte(updated))
	state := fileStateJSON(struct {
		Path         string `json:"path"`
		SizeBytes    int    `json:"size_bytes"`
		BeforeDigest string `json:"before_digest"`
		AfterDigest  string `json:"after_digest"`
		Replacements int    `json:"replacements"`
		DryRun       bool   `json:"dry_run"`
	}{Path: path, SizeBytes: len(updated), BeforeDigest: beforeDigest, AfterDigest: afterDigest, Replacements: replacementCount, DryRun: input.DryRun})
	if updated == content {
		result := tool.TextResult(fmt.Sprintf("No content change was needed in %q.", path))
		result.State = state
		return result, nil
	}
	if input.DryRun {
		return tool.Result{
			Content: []message.Content{{Type: message.ContentText, Text: fmt.Sprintf(
				"Dry run: would replace %d occurrence(s) in %q. before_digest=%s after_digest=%s.",
				replacementCount, path, beforeDigest, afterDigest,
			)}},
			State: state,
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if err := e.fileSystem.WriteFile(ctx, path, []byte(updated), 0o644); err != nil {
		return tool.Result{}, fileSystemError("write", err)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	result := tool.TextResult(fmt.Sprintf(
		"Replaced %d occurrence(s) in %q. before_digest=%s after_digest=%s.",
		replacementCount, path, beforeDigest, afterDigest,
	))
	result.State = state
	return result, nil
}
