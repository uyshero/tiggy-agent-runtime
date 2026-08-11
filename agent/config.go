// Package agent 实现有界、可观测的 ReAct Runtime。
//
// Agent 持有不可变 Config；每次 Run 使用 Toolkit 快照、事件发射器与 State，
// 并按 Limits / FailurePolicy 约束模型调用与工具执行。
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/approval"
	"github.com/uyshero/tiggy-agent-runtime/artifact"
	"github.com/uyshero/tiggy-agent-runtime/contextassembly"
	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/memory"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/skill"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

// IDGenerator 生成带前缀的稳定标识（消息、事件、Run 等）。
// 默认实现是 randomID；测试可注入确定性序列。
type IDGenerator func(prefix string) string

// Clock 可注入的时钟，便于测试；Runtime 会转为 UTC。
type Clock func() time.Time

// Limits 约束单次 Run 的迭代、调用次数、Token 与墙钟时长。
// New 会为完整零值及新增流式字段补默认值；规范化后所有字段都必须为正。
type Limits struct {
	MaxIterations             int           // ReAct 循环最大轮数（每轮一次模型调用）
	MaxModelCalls             int           // 模型调用总次数上限（含重试/降级）
	MaxToolCalls              int           // 工具调用总次数上限
	MaxInputTokens            int64         // 累计输入 Token 上限
	MaxOutputTokens           int64         // 累计输出 Token 上限
	MaxReasoningTokens        int64         // 累计推理 Token 上限
	MaxOutputTokensPerCall    int           // 单次模型请求允许的最大输出 Token
	MaxModelDeltasPerCall     int           // 单次模型调用最多接受的流式 Delta 数
	MaxModelDeltaBytesPerCall int64         // 单次模型调用接受的 Delta UTF-8 字符串载荷累计字节数
	MaxDuration               time.Duration // 单次 Run 墙钟超时（独立于调用方 Context）
}

// DefaultLimits 返回标准有界策略（偏保守的开发/演示默认值）。
func DefaultLimits() Limits {
	return Limits{
		MaxIterations:             20,
		MaxModelCalls:             20,
		MaxToolCalls:              64,
		MaxInputTokens:            1_000_000,
		MaxOutputTokens:           1_000_000,
		MaxReasoningTokens:        1_000_000,
		MaxOutputTokensPerCall:    4096,
		MaxModelDeltasPerCall:     10_000,
		MaxModelDeltaBytesPerCall: 16 << 20,
		MaxDuration:               10 * time.Minute,
	}
}

// normalizeLimits 为 v0.1.0 之后新增的流式边界填默认值，使旧的显式 Limits 保持兼容。
func normalizeLimits(limits Limits) Limits {
	defaults := DefaultLimits()
	if limits == (Limits{}) {
		return defaults
	}
	if limits.MaxModelDeltasPerCall == 0 {
		limits.MaxModelDeltasPerCall = defaults.MaxModelDeltasPerCall
	}
	if limits.MaxModelDeltaBytesPerCall == 0 {
		limits.MaxModelDeltaBytesPerCall = defaults.MaxModelDeltaBytesPerCall
	}
	return limits
}

// validate 要求所有限额字段为正；零值不允许（须先填 DefaultLimits）。
func (l Limits) validate() error {
	if l.MaxIterations <= 0 || l.MaxModelCalls <= 0 || l.MaxToolCalls <= 0 {
		return fmt.Errorf("iteration, model-call, and tool-call limits must be positive")
	}
	if l.MaxInputTokens <= 0 || l.MaxOutputTokens <= 0 || l.MaxReasoningTokens <= 0 {
		return fmt.Errorf("token limits must be positive")
	}
	if l.MaxOutputTokensPerCall <= 0 || l.MaxDuration <= 0 {
		return fmt.Errorf("per-call output and duration limits must be positive")
	}
	if l.MaxModelDeltasPerCall <= 0 || l.MaxModelDeltaBytesPerCall <= 0 {
		return fmt.Errorf("per-call model delta limits must be positive")
	}
	return nil
}

// Config 描述一次可复用的 Agent 绑定（模型、工具、记忆、审批与边界）。
//
// 由应用在进程内构造并注入；Runtime 不读取全局凭证或环境变量。
// New 成功后 Config 被拷贝进 Agent，字段在 Run 期间视为只读。
type Config struct {
	// Name 出现在 run.started 等事件中，必须非空。
	Name string
	// SystemPrompt 首轮写入内部 System 消息；可与 Skills 指令拼接。
	SystemPrompt string
	// Instructions 是应用注入的额外静态指令段，例如项目知识、任务规则和策略说明。
	// 它们进入 InstructionStack 的动态上下文边界之上；动态证据不得从这里注入。
	Instructions []contextassembly.Segment

	// Model 主模型适配器（必填）；ModelIdentity 描述该绑定的供应商/型号。
	Model         model.Model
	ModelIdentity model.Identity

	// ContextPlanner 在模型调用前做 Token 预算；nil 表示跳过规划。
	ContextPlanner contextmanager.Planner
	// ContextCompressor 在预算溢出时压缩上下文，也可供显式的 Provider Context Length 恢复策略使用；
	// 配置了它则必须同时有 Planner。
	ContextCompressor contextmanager.Compressor

	// ArtifactOffloader 把超限 Tool Result 卸载到外部 Store；nil 则始终行内返回。
	ArtifactOffloader artifact.Offloader

	// Memory 长期记忆端口；nil 表示禁用。启用时必须提供合法 Scope，Options 会被规范化。
	Memory        memory.Port
	MemoryScope   memory.Scope
	MemoryOptions memory.Options

	// Fallbacks 主模型失败后的候选目标；非空时必须配置 FailurePolicy。
	Fallbacks []ModelTarget
	// FailurePolicy 决定重试/降级/失败；nil 时默认 fail-fast。
	FailurePolicy ModelFailurePolicy

	// Toolkit 工具注册表；按 ToolGroups/Skills 投影。仅配置分组时必填。
	Toolkit *toolkit.Toolkit
	// ApprovalPolicy 整批工具审批；nil 时审批层按默认策略处理（见 approval 包）。
	ApprovalPolicy approval.Policy
	// ToolGroups 本次 Agent 允许暴露的工具分组；与 Skills 的分组取并集。
	ToolGroups []string
	// Skills 已显式激活的不可变 Skill 快照；提供额外指令与 ToolGroups。
	Skills *skill.Activation

	// TransitionHook 可选地同步持久化关键执行边界；错误或 panic 会失败关闭并停止推进。
	TransitionHook TransitionHook
	// ToolEffectClasses 按模型可见 Tool 名声明外部效果语义。未声明项按不可对账写处理。
	ToolEffectClasses map[string]ToolEffectClass

	// Limits 单次 Run 边界；零值用 DefaultLimits。
	Limits Limits
	// IDs / Now 可注入，便于确定性测试；nil 时用 randomID / time.Now。
	IDs IDGenerator
	Now Clock
}

// Agent 是可并发复用的 Runtime 入口；每次 Run 使用独立 State 与事件序列。
// 内部只保存一份校验后的 Config 副本。
type Agent struct {
	config Config
}

// New 校验并规范化 Config，构造可复用 Agent。
// 失败时返回 error，不会留下半初始化实例。
func New(config Config) (*Agent, error) {
	// ---- 基础必填 ----
	config.Name = strings.TrimSpace(config.Name)
	if config.Name == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	if config.Model == nil {
		return nil, fmt.Errorf("model is required")
	}
	if err := config.ModelIdentity.Validate(); err != nil {
		return nil, err
	}
	config.Instructions = contextassembly.CloneSegments(config.Instructions)
	if _, _, err := contextassembly.Render(config.Instructions); err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}

	// ---- Fallback：拷贝切片，并与主模型的 Planner 配置保持一致 ----
	config.Fallbacks = append([]ModelTarget(nil), config.Fallbacks...)
	for index, target := range config.Fallbacks {
		if target.Model == nil {
			return nil, fmt.Errorf("fallback model %d is required", index)
		}
		if err := target.Identity.Validate(); err != nil {
			return nil, fmt.Errorf("fallback model %d: %w", index, err)
		}
		// 主模型有/无 Planner，每个 Fallback 必须同样有/无，避免降级后行为分裂。
		if (config.ContextPlanner == nil) != (target.ContextPlanner == nil) {
			return nil, fmt.Errorf("fallback model %d must configure ContextPlanner consistently with the primary model", index)
		}
		if target.ContextCompressor != nil && target.ContextPlanner == nil {
			return nil, fmt.Errorf("fallback model %d requires ContextPlanner when ContextCompressor is configured", index)
		}
	}
	if config.ContextCompressor != nil && config.ContextPlanner == nil {
		return nil, fmt.Errorf("ContextPlanner is required when ContextCompressor is configured")
	}

	// ---- Memory：要么全关，要么 Port + Scope + 规范化 Options ----
	if config.Memory == nil {
		if config.MemoryScope != (memory.Scope{}) || config.MemoryOptions != (memory.Options{}) {
			return nil, fmt.Errorf("Memory is required when MemoryScope or MemoryOptions is configured")
		}
	} else {
		if err := config.MemoryScope.Validate(); err != nil {
			return nil, fmt.Errorf("memory scope: %w", err)
		}
		options, err := memory.NormalizeOptions(config.MemoryOptions)
		if err != nil {
			return nil, err
		}
		config.MemoryOptions = options
	}

	// ---- 失败策略：有 Fallback 必须显式 Policy；否则默认立即失败 ----
	if len(config.Fallbacks) > 0 && config.FailurePolicy == nil {
		return nil, fmt.Errorf("failure policy is required when fallback models are configured")
	}
	if config.FailurePolicy == nil {
		config.FailurePolicy = failFastModelPolicy{}
	}

	// ---- 工具投影：配置分组与 Skill 分组取并集，并提前校验 Toolkit 能快照 ----
	config.ToolGroups = canonicalToolGroups(config.ToolGroups)
	var effectClassErr error
	config.ToolEffectClasses, effectClassErr = normalizeToolEffectClasses(config.ToolEffectClasses)
	if effectClassErr != nil {
		return nil, effectClassErr
	}
	config.Skills = config.Skills.Clone() // 避免调用方之后改动激活快照
	effectiveGroups := effectiveToolGroups(config.ToolGroups, config.Skills)
	if len(effectiveGroups) > 0 {
		if config.Toolkit == nil {
			return nil, fmt.Errorf("toolkit is required when Agent or Skill tool groups are configured")
		}
		// 构造期就 SnapshotGroups，尽早发现未知 Group。
		if _, err := config.Toolkit.SnapshotGroups(effectiveGroups...); err != nil {
			return nil, fmt.Errorf("tool groups: %w", err)
		}
	}

	// ---- Limits / ID / Clock 默认值 ----
	config.Limits = normalizeLimits(config.Limits)
	if err := config.Limits.validate(); err != nil {
		return nil, err
	}
	if config.IDs == nil {
		config.IDs = randomID
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	return &Agent{config: config}, nil
}

func normalizeToolEffectClasses(values map[string]ToolEffectClass) (map[string]ToolEffectClass, error) {
	if len(values) == 0 {
		return nil, nil
	}
	cloned := make(map[string]ToolEffectClass, len(values))
	for original, class := range values {
		name := strings.TrimSpace(original)
		if name == "" {
			return nil, fmt.Errorf("Tool effect class name is required")
		}
		if _, exists := cloned[name]; exists {
			return nil, fmt.Errorf("duplicate Tool effect class for %q", name)
		}
		if err := class.validate(); err != nil {
			return nil, fmt.Errorf("Tool %q: %w", name, err)
		}
		cloned[name] = class
	}
	return cloned, nil
}

func (a *Agent) toolEffectClass(name string) ToolEffectClass {
	if class, exists := a.config.ToolEffectClasses[name]; exists {
		return class
	}
	return ToolEffectNonReconcilableWrite
}

// effectiveToolGroups 合并 Agent 配置的 ToolGroups 与已激活 Skill 声明的分组（排序去重）。
func effectiveToolGroups(configured []string, active *skill.Activation) []string {
	unique := make(map[string]struct{}, len(configured))
	for _, group := range configured {
		unique[group] = struct{}{}
	}
	for _, group := range active.ToolGroups() {
		unique[group] = struct{}{}
	}
	groups := make([]string, 0, len(unique))
	for group := range unique {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

// canonicalToolGroups 去掉分组名首尾空白（不改顺序、不去重；去重在 effectiveToolGroups）。
func canonicalToolGroups(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	return result
}

// randomID 默认 ID 生成器：prefix_ + 12 字节十六进制；读随机数失败时退回纳秒时间戳。
func randomID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}
