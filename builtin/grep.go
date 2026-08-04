package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

// GrepToolName 是 GrepTool 对模型可见的稳定名称。
const GrepToolName = "builtin.grep"

// GrepOptions 配置 GrepTool；Permission 为 nil 时拒绝所有调用。
type GrepOptions struct {
	FileSystem safespace.FileSystem
	Permission permission.Policy
	Limits     SearchLimits
}

// GrepTool 返回有界的按行正则匹配结果。
type GrepTool struct {
	fileSystem safespace.FileSystem
	permission permission.Policy
	limits     SearchLimits
}

var _ tool.Tool = (*GrepTool)(nil)

// NewGrep constructs a bounded recursive text searcher.
func NewGrep(options GrepOptions) (*GrepTool, error) {
	if options.FileSystem == nil {
		return nil, fmt.Errorf("grep tool filesystem is required")
	}
	limits, err := normalizeSearchLimits(options.Limits)
	if err != nil {
		return nil, err
	}
	return &GrepTool{fileSystem: options.FileSystem, permission: options.Permission, limits: limits}, nil
}

func (g *GrepTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:             GrepToolName,
		Description:      "Search UTF-8 text files with a Go regular expression.",
		ConcurrencyClass: model.ToolConcurrencySequential,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","maxLength":4096},
				"pattern":{"type":"string","minLength":1,"maxLength":4096},
				"include":{"type":"string","minLength":1,"maxLength":4096}
			},
			"required":["pattern"],
			"additionalProperties":false
		}`),
	}
}

func (g *GrepTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var input struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		Include string `json:"include"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return tool.Result{}, err
	}
	root, err := normalizedSearchRoot(input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	if input.Pattern == "" || len(input.Pattern) > maximumPathBytes {
		return tool.Result{}, invalidArguments(fmt.Errorf("regular expression must contain between 1 and %d bytes", maximumPathBytes))
	}
	expression, err := regexp.Compile(input.Pattern)
	if err != nil {
		return tool.Result{}, tool.NewError("invalid_pattern", "The regular expression is invalid.", false, err)
	}
	include := input.Include
	if include == "" {
		include = "**"
	}
	include, err = validatedGlobPattern(include)
	if err != nil {
		return tool.Result{}, err
	}
	if err := authorize(ctx, g.permission, GrepToolName, permission.ActionSearch, root); err != nil {
		return tool.Result{}, err
	}
	results := boundedResults{limits: g.limits}
	skipped := 0
	walkErr := walkFileSystem(ctx, g.fileSystem, root, g.limits, func(entry walkedEntry) error {
		if entry.IsDir || !doublestar.MatchUnvalidated(include, entry.Relative) {
			return nil
		}
		if entry.Size > g.limits.MaxFileBytes {
			skipped++
			return nil
		}
		data, err := g.fileSystem.ReadFile(ctx, entry.Path)
		if err != nil {
			classified := fileSystemError("read", err)
			var runtimeError *tool.RuntimeError
			if errors.As(classified, &runtimeError) && runtimeError != nil {
				return runtimeError
			}
			if errors.Is(classified, context.Canceled) || errors.Is(classified, context.DeadlineExceeded) {
				return classified
			}
			skipped++
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if int64(len(data)) > g.limits.MaxFileBytes || !utf8.Valid(data) {
			skipped++
			return nil
		}
		for lineNumber, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if !expression.MatchString(line) {
				continue
			}
			line = truncateUTF8(line, maximumGrepLineBytes)
			value := fmt.Sprintf("%s:%d:%s", strconv.Quote(entry.Path), lineNumber+1, line)
			if !results.add(value) {
				return errStopSearch
			}
		}
		return nil
	})
	if err := searchFailure(walkErr); err != nil {
		return tool.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	summary := fmt.Sprintf("Found %d matching line(s); skipped %d unreadable, oversized, or non-UTF-8 file(s).", len(results.values), skipped)
	if results.truncated {
		summary = fmt.Sprintf("Found at least %d matching line(s); results were truncated; skipped %d file(s).", len(results.values), skipped)
	}
	contents := []message.Content{{Type: message.ContentText, Text: summary}}
	if len(results.values) > 0 {
		contents = append(contents, message.Content{Type: message.ContentText, Text: strings.Join(results.values, "\n")})
	}
	return tool.Result{Content: contents}, nil
}
