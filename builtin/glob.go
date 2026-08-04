package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

// GlobToolName 是 GlobTool 对模型可见的稳定名称。
const GlobToolName = "builtin.glob"

// GlobOptions 配置 GlobTool；Permission 为 nil 时拒绝所有调用。
type GlobOptions struct {
	FileSystem safespace.FileSystem
	Permission permission.Policy
	Limits     SearchLimits
}

// GlobTool 返回匹配 doublestar 模式的有界路径列表。
type GlobTool struct {
	fileSystem safespace.FileSystem
	permission permission.Policy
	limits     SearchLimits
}

var _ tool.Tool = (*GlobTool)(nil)

// NewGlob 构造有界递归路径匹配工具。
func NewGlob(options GlobOptions) (*GlobTool, error) {
	if options.FileSystem == nil {
		return nil, fmt.Errorf("glob tool filesystem is required")
	}
	limits, err := normalizeSearchLimits(options.Limits)
	if err != nil {
		return nil, err
	}
	return &GlobTool{fileSystem: options.FileSystem, permission: options.Permission, limits: limits}, nil
}

func (g *GlobTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:             GlobToolName,
		Description:      "Find files and directories matching a glob pattern in the configured file space.",
		ConcurrencyClass: model.ToolConcurrencySequential,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","maxLength":4096},
				"pattern":{"type":"string","minLength":1,"maxLength":4096}
			},
			"required":["pattern"],
			"additionalProperties":false
		}`),
	}
}

func (g *GlobTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var input struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return tool.Result{}, err
	}
	root, err := normalizedSearchRoot(input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	pattern, err := validatedGlobPattern(input.Pattern)
	if err != nil {
		return tool.Result{}, err
	}
	if err := authorize(ctx, g.permission, GlobToolName, permission.ActionSearch, root); err != nil {
		return tool.Result{}, err
	}
	results := boundedResults{limits: g.limits}
	walkErr := walkFileSystem(ctx, g.fileSystem, root, g.limits, func(entry walkedEntry) error {
		candidate := entry.Relative
		matched := doublestar.MatchUnvalidated(pattern, candidate)
		if entry.IsDir && !matched {
			matched = doublestar.MatchUnvalidated(pattern, candidate+"/")
		}
		if !matched {
			return nil
		}
		value := entry.Path
		if entry.IsDir {
			value += "/"
		}
		if !results.add(value) {
			return errStopSearch
		}
		return nil
	})
	if err := searchFailure(walkErr); err != nil {
		return tool.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	summary := fmt.Sprintf("Matched %d path(s).", len(results.values))
	if results.truncated {
		summary = fmt.Sprintf("Matched at least %d path(s); results were truncated.", len(results.values))
	}
	contents := []message.Content{{Type: message.ContentText, Text: summary}}
	if len(results.values) > 0 {
		contents = append(contents, message.Content{Type: message.ContentText, Text: strings.Join(results.values, "\n")})
	}
	return tool.Result{Content: contents}, nil
}
