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
	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/memory"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/skill"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

// IDGenerator 生成带前缀的稳定标识（消息、事件、Run 等）。
type IDGenerator func(prefix string) string

// Clock 可注入的时钟，便于测试；Runtime 会转为 UTC。
type Clock func() time.Time

// Limits 约束单次 Run 的迭代、调用次数、Token 与墙钟时长。
type Limits struct {
	MaxIterations          int
	MaxModelCalls          int
	MaxToolCalls           int
	MaxInputTokens         int64
	MaxOutputTokens        int64
	MaxReasoningTokens     int64
	MaxOutputTokensPerCall int
	MaxDuration            time.Duration
}

// DefaultLimits 返回标准有界策略。
func DefaultLimits() Limits {
	return Limits{
		MaxIterations:          20,
		MaxModelCalls:          20,
		MaxToolCalls:           64,
		MaxInputTokens:         1_000_000,
		MaxOutputTokens:        1_000_000,
		MaxReasoningTokens:     1_000_000,
		MaxOutputTokensPerCall: 4096,
		MaxDuration:            10 * time.Minute,
	}
}

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
	return nil
}

// Config 描述一次可复用的 Agent 绑定（模型、工具、记忆、审批与边界）。
type Config struct {
	Name              string
	SystemPrompt      string
	Model             model.Model
	ModelIdentity     model.Identity
	ContextPlanner    contextmanager.Planner
	ContextCompressor contextmanager.Compressor
	ArtifactOffloader artifact.Offloader
	Memory            memory.Port
	MemoryScope       memory.Scope
	MemoryOptions     memory.Options
	Fallbacks         []ModelTarget
	FailurePolicy     ModelFailurePolicy
	Toolkit           *toolkit.Toolkit
	ApprovalPolicy    approval.Policy
	ToolGroups        []string
	Skills            *skill.Activation
	Limits            Limits
	IDs               IDGenerator
	Now               Clock
}

// Agent 是可并发复用的 Runtime 入口；每次 Run 使用独立 State 与事件序列。
type Agent struct {
	config Config
}

// New 校验并规范化 Config，构造可复用 Agent。
func New(config Config) (*Agent, error) {
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
	config.Fallbacks = append([]ModelTarget(nil), config.Fallbacks...)
	for index, target := range config.Fallbacks {
		if target.Model == nil {
			return nil, fmt.Errorf("fallback model %d is required", index)
		}
		if err := target.Identity.Validate(); err != nil {
			return nil, fmt.Errorf("fallback model %d: %w", index, err)
		}
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
	if len(config.Fallbacks) > 0 && config.FailurePolicy == nil {
		return nil, fmt.Errorf("failure policy is required when fallback models are configured")
	}
	if config.FailurePolicy == nil {
		config.FailurePolicy = failFastModelPolicy{}
	}
	config.ToolGroups = canonicalToolGroups(config.ToolGroups)
	config.Skills = config.Skills.Clone()
	effectiveGroups := effectiveToolGroups(config.ToolGroups, config.Skills)
	if len(effectiveGroups) > 0 {
		if config.Toolkit == nil {
			return nil, fmt.Errorf("toolkit is required when Agent or Skill tool groups are configured")
		}
		if _, err := config.Toolkit.SnapshotGroups(effectiveGroups...); err != nil {
			return nil, fmt.Errorf("tool groups: %w", err)
		}
	}
	if config.Limits == (Limits{}) {
		config.Limits = DefaultLimits()
	}
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

func canonicalToolGroups(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	return result
}

func randomID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}
