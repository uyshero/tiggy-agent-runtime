// Package toolkit 负责注册工具，并为每次 Run 生成不可变快照 View。
//
// 关键路径：
//
//	RegisterWith → 读 Definition → 编译 Input Schema → 写入注册表
//	Snapshot / SnapshotGroups → 生成 View（Run 期间不变）
//	View.Call → 校验参数 → Execute → 校验 Result
//
// Group 只用于能力投影，不进入 Tool Call 协议名。
package toolkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

// argumentValidator 是已编译 JSON Schema 的校验入口。
type argumentValidator interface {
	Validate(any) error
}

// registeredTool 保存工具实例、Definition 副本、Schema 校验器与所属 Groups。
type registeredTool struct {
	tool       tool.Tool
	definition model.ToolDefinition // 注册时拷贝；Name 已是限定名
	validator  argumentValidator
	groups     map[string]struct{}
}

// Registration 描述一次注册：可选 Namespace 前缀与 Groups 投影标签。
// 例：Namespace="crm", 原名 lookup → 模型可见名 "crm.lookup"。
type Registration struct {
	Tool      tool.Tool
	Namespace string
	Groups    []string
}

// Toolkit 是并发安全的工具注册表；Agent 可复用同一 Toolkit，每次 Run 各自 Snapshot。
type Toolkit struct {
	mu    sync.RWMutex
	tools map[string]registeredTool // key = 限定名
}

// New 构造 Toolkit 并按序注册初始工具；任一注册失败则整体失败。
func New(values ...tool.Tool) (*Toolkit, error) {
	kit := &Toolkit{tools: make(map[string]registeredTool, len(values))}
	for _, value := range values {
		if err := kit.Register(value); err != nil {
			return nil, err
		}
	}
	return kit, nil
}

// Register 使用空 Namespace 注册单个工具。
func (t *Toolkit) Register(value tool.Tool) error {
	return t.RegisterWith(Registration{Tool: value})
}

// RegisterWith 读取 Definition、编译 Schema，并以限定名写入注册表。
// 同名已存在则报错；编译失败拒绝注册（不会留下半条目）。
func (t *Toolkit) RegisterWith(registration Registration) error {
	if registration.Tool == nil {
		return fmt.Errorf("tool is required")
	}
	definition, err := readDefinition(registration.Tool)
	if err != nil {
		return err
	}
	definition = definition.Clone()
	if err := definition.Validate(); err != nil {
		return err
	}
	name, err := qualifiedName(registration.Namespace, definition.Name)
	if err != nil {
		return err
	}
	definition.Name = name // 对外只暴露限定名
	groups, err := normalizeGroups(registration.Groups)
	if err != nil {
		return err
	}
	validator, err := compileInputSchema(name, definition.InputSchema)
	if err != nil {
		return fmt.Errorf("compile input schema for tool %q: %w", name, err)
	}

	entry := registeredTool{
		tool: registration.Tool, definition: definition.Clone(), validator: validator, groups: groups,
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.tools[name]; exists {
		return fmt.Errorf("tool %q is already registered", name)
	}
	t.tools[name] = entry
	return nil
}

// Groups 返回当前已注册工具涉及的全部 Group 名称（排序去重）。
func (t *Toolkit) Groups() []string {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	unique := make(map[string]struct{})
	for _, entry := range t.tools {
		for group := range entry.groups {
			unique[group] = struct{}{}
		}
	}
	groups := make([]string, 0, len(unique))
	for group := range unique {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

// View 是一次 Run 持有的不可变注册快照；之后新注册只影响后续 Run。
type View struct {
	tools       map[string]registeredTool
	definitions []model.ToolDefinition // 按名称排序，供模型请求
}

// Snapshot 返回全量工具快照；Toolkit 为 nil 时返回空 View。
func (t *Toolkit) Snapshot() View {
	if t == nil {
		return emptyView()
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return snapshot(t.tools, nil)
}

// SnapshotGroups 返回所请求 Group 的并集。
// 未知 Group 直接报错，避免配置笔误静默丢掉能力。
func (t *Toolkit) SnapshotGroups(groups ...string) (View, error) {
	if t == nil {
		return View{}, fmt.Errorf("toolkit is required")
	}
	requested, err := normalizeGroups(groups)
	if err != nil {
		return View{}, err
	}
	if len(requested) == 0 {
		return emptyView(), nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	known := make(map[string]struct{})
	for _, entry := range t.tools {
		for group := range entry.groups {
			known[group] = struct{}{}
		}
	}
	for group := range requested {
		if _, exists := known[group]; !exists {
			return View{}, fmt.Errorf("tool group %q is not registered", group)
		}
	}
	return snapshot(t.tools, requested), nil
}

// Definitions 返回本快照中工具定义的深拷贝（供模型 Request.Tools）。
func (v View) Definitions() []model.ToolDefinition {
	values := make([]model.ToolDefinition, len(v.definitions))
	for index, definition := range v.definitions {
		values[index] = definition.Clone()
	}
	return values
}

// DefinitionDigest 对本 View 中模型可见 Tool 契约做指纹。
// 审批续跑用它拒绝「挂起后 Toolkit 定义已变」的 Resume。
func (v View) DefinitionDigest(name string) (string, bool) {
	entry, exists := v.tools[name]
	if !exists {
		return "", false
	}
	encoded, err := json.Marshal(entry.definition)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), true
}

// Call 执行单次工具调用：校验参数 → Execute → 校验 Result。
//
// 错误分类：
//   - 参数/未知工具 → tool.Error（模型可修正）
//   - Schema 校验器 panic / 工具 panic / 非法 Result → tool.RuntimeError
//   - 工具返回的 Error / RuntimeError 原样透传
func (v View) Call(ctx context.Context, call message.ToolCall) (result tool.Result, err error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if err := call.Validate(); err != nil {
		return tool.Result{}, tool.NewError("invalid_tool_arguments", "The tool arguments are invalid JSON.", false, err)
	}
	if strings.TrimSpace(call.ArgumentsError) != "" {
		// 模型侧已标明参数解析失败（例如非 JSON），不再二次解码。
		return tool.Result{}, tool.NewError("invalid_tool_arguments", "The tool arguments could not be parsed.", false, errors.New(call.ArgumentsError))
	}
	entry, exists := v.tools[call.Name]
	if !exists {
		return tool.Result{}, tool.NewError("unsupported_tool", "The requested tool is not available.", false, nil)
	}
	instance, err := decodeJSON(call.Arguments)
	if err != nil {
		return tool.Result{}, tool.NewError("invalid_tool_arguments", "The tool arguments are invalid JSON.", false, err)
	}
	if err := validateInput(entry.validator, instance); err != nil {
		var panicError *schemaPanicError
		if errors.As(err, &panicError) {
			return tool.Result{}, &tool.RuntimeError{Code: "schema_validation_failed", Cause: err}
		}
		return tool.Result{}, tool.NewError("invalid_tool_arguments", "The tool arguments do not match the required schema.", false, err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &tool.RuntimeError{
				Code:  "tool_panic",
				Cause: fmt.Errorf("panic: %v\n%s", recovered, debug.Stack()),
			}
		}
	}()
	// 传入 Arguments 副本，防止工具改写调用方缓冲。
	result, err = entry.tool.Execute(ctx, append(json.RawMessage(nil), call.Arguments...))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return tool.Result{}, ctxErr
	}
	if err != nil {
		var runtimeFailure *tool.RuntimeError
		if errors.As(err, &runtimeFailure) {
			return tool.Result{}, runtimeFailure
		}
		var safe *tool.Error
		if errors.As(err, &safe) {
			return tool.Result{}, safe
		}
		// 未分类 error 一律升为 RuntimeError，避免把内部细节回传模型。
		return tool.Result{}, &tool.RuntimeError{Code: "tool_execution_failed", Cause: err}
	}
	validation := message.ToolResult{CallID: call.ID, Name: call.Name, Content: result.Content, State: result.State}
	if err := validation.Validate(); err != nil {
		return tool.Result{}, &tool.RuntimeError{Code: "invalid_tool_result", Cause: err}
	}
	result.Content = message.CloneContent(result.Content)
	result.State = append(json.RawMessage(nil), result.State...)
	return result, nil
}

// snapshot 按可选 groups 过滤并拷贝定义；groups==nil 表示全量。
func snapshot(values map[string]registeredTool, groups map[string]struct{}) View {
	view := View{tools: make(map[string]registeredTool)}
	for name, entry := range values {
		if len(groups) > 0 && !inAnyGroup(entry.groups, groups) {
			continue
		}
		view.tools[name] = entry
		view.definitions = append(view.definitions, entry.definition.Clone())
	}
	sort.Slice(view.definitions, func(first, second int) bool {
		return view.definitions[first].Name < view.definitions[second].Name
	})
	return view
}

func emptyView() View {
	return View{tools: map[string]registeredTool{}}
}

func inAnyGroup(toolGroups, requested map[string]struct{}) bool {
	for group := range requested {
		if _, exists := toolGroups[group]; exists {
			return true
		}
	}
	return false
}

func normalizeGroups(values []string) (map[string]struct{}, error) {
	groups := make(map[string]struct{}, len(values))
	for _, value := range values {
		group := strings.TrimSpace(value)
		if err := validateQualifiedName("tool group", group, 64); err != nil {
			return nil, err
		}
		groups[group] = struct{}{}
	}
	return groups, nil
}

func qualifiedName(namespace, name string) (string, error) {
	name = strings.TrimSpace(name)
	if err := validateQualifiedName("tool name", name, 128); err != nil {
		return "", err
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return name, nil
	}
	if err := validateQualifiedName("tool namespace", namespace, 64); err != nil {
		return "", err
	}
	qualified := namespace + "." + name
	if len(qualified) > 128 {
		return "", fmt.Errorf("qualified tool name exceeds 128 bytes")
	}
	return qualified, nil
}

func validateQualifiedName(kind, value string, maximum int) error {
	if value == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", kind, maximum)
	}
	for _, segment := range strings.Split(value, ".") {
		if segment == "" {
			return fmt.Errorf("%s %q contains an empty segment", kind, value)
		}
		for index, char := range []byte(segment) {
			allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-'
			if !allowed || index == 0 && char == '-' {
				return fmt.Errorf("%s %q contains an unsupported character", kind, value)
			}
		}
	}
	return nil
}

func readDefinition(value tool.Tool) (definition model.ToolDefinition, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tool definition panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return value.Definition(), nil
}
