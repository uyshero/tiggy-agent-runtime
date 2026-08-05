// Package model 定义供应商无关的模型生成契约。
//
// Agent 只依赖本包接口与类型；具体 HTTP/SDK 细节留在供应商适配器里。
package model

import (
	"context"
	"encoding/json"

	"github.com/uyshero/tiggy-agent-runtime/message"
)

// DeltaSink 按流式顺序同步回调；返回错误即要求供应商停止生成并把错误交回调用方。
type DeltaSink func(Delta) error

// Model 是供应商无关的生成契约。
// Generate 必须在返回前走完流（或失败），不得在返回后调用 DeltaSink；
// DeltaSink 可为 nil（无流式），返回 error 时 Adapter 必须停止生成并返回。
type Model interface {
	Generate(context.Context, Request, DeltaSink) (Response, error)
}

// TokenCounter 是上下文规划使用的可选能力（ContextManager 会探测）。
type TokenCounter interface {
	CountTokens(context.Context, TokenRequest) (int64, error)
}

// RequestPurpose 标明一次生成请求的用途，便于观察与计费归因。
type RequestPurpose string

const (
	PurposeAgent      RequestPurpose = "agent"      // 主循环模型调用
	PurposeCompaction RequestPurpose = "compaction" // 上下文压缩
	PurposeMemory     RequestPurpose = "memory"     // 记忆侧车
	PurposeEvaluation RequestPurpose = "evaluation" // 评估/旁路
)

// Identity 描述单次请求绑定的模型；凭据留在供应商实现内，不得放入本值。
type Identity struct {
	Provider string `json:"provider"`           // 如 anthropic / openai
	Model    string `json:"model"`              // 模型名
	Revision string `json:"revision,omitempty"` // 可选版本钉扎
}

// ToolConcurrencyClass 声明工具在 CallBatch 中的并发类别。
type ToolConcurrencyClass string

const (
	ToolConcurrencySequential ToolConcurrencyClass = "sequential" // 默认：顺序屏障
	ToolConcurrencySafe       ToolConcurrencyClass = "safe"       // 可与同类并行
)

// ToolDefinition 是暴露给模型的工具定义（含 Input Schema）。
// OutputSchema 预留，当前 Runtime 不强制校验输出。
type ToolDefinition struct {
	Name             string               `json:"name"`
	Description      string               `json:"description,omitempty"`
	InputSchema      json.RawMessage      `json:"input_schema"`
	OutputSchema     json.RawMessage      `json:"output_schema,omitempty"`
	ConcurrencyClass ToolConcurrencyClass `json:"concurrency_class,omitempty"`
}

// Clone 深拷贝 Schema 字节，避免注册表与调用方共享缓冲。
func (d ToolDefinition) Clone() ToolDefinition {
	cloned := d
	cloned.InputSchema = append(json.RawMessage(nil), d.InputSchema...)
	cloned.OutputSchema = append(json.RawMessage(nil), d.OutputSchema...)
	return cloned
}

// Request 是一次 Generate 的完整输入。
type Request struct {
	RunID           string            `json:"run_id"`
	CallID          string            `json:"call_id"` // 本轮模型调用 ID
	Purpose         RequestPurpose    `json:"purpose"`
	Model           Identity          `json:"model"`
	Messages        []message.Message `json:"messages"`
	Tools           []ToolDefinition  `json:"tools,omitempty"` // 本轮可用工具
	MaxOutputTokens int               `json:"max_output_tokens"`
	Parameters      json.RawMessage   `json:"parameters,omitempty"` // 供应商扩展参数
}

// TokenRequest 是 CountTokens 的输入（不含完整 Generate 元数据）。
type TokenRequest struct {
	Model    Identity          `json:"model"`
	Messages []message.Message `json:"messages"`
	Tools    []ToolDefinition  `json:"tools,omitempty"`
}

// StopReason 说明模型为何结束本轮生成。
type StopReason string

const (
	StopReasonComplete StopReason = "complete"  // 正常说完
	StopReasonToolCall StopReason = "tool_call" // 需要执行工具
	StopReasonLength   StopReason = "length"    // 触达输出上限
	StopReasonCanceled StopReason = "canceled"
	StopReasonError    StopReason = "error"
)

// Response 是 Generate 的最终结果（流式结束后也应给出）。
type Response struct {
	Message           message.Message `json:"message"` // 助手消息（可含 ToolCall）
	StopReason        StopReason      `json:"stop_reason"`
	ProviderRequestID string          `json:"provider_request_id,omitempty"`
	Usage             Usage           `json:"usage"`
}

// DeltaType 标识流式增量种类。
type DeltaType string

const (
	DeltaStarted  DeltaType = "started"
	DeltaText     DeltaType = "text"
	DeltaThinking DeltaType = "thinking"
	DeltaToolCall DeltaType = "tool_call"
	DeltaUsage    DeltaType = "usage"
	DeltaStopped  DeltaType = "stopped"
	DeltaError    DeltaType = "error"
)

// Delta 是流式过程中的一次增量事件。
type Delta struct {
	Type          DeltaType      `json:"type"`
	Index         int            `json:"index"` // 同类型内容块序号
	Text          string         `json:"text,omitempty"`
	ToolCall      *ToolCallDelta `json:"tool_call,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	StopReason    StopReason     `json:"stop_reason,omitempty"`
	ProviderError *ProviderError `json:"provider_error,omitempty"`
}

// ToolCallDelta 是工具调用的流式片段（ID/Name/Arguments 可能分多次到达）。
type ToolCallDelta struct {
	Index             int    `json:"index"`
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	ArgumentsFragment string `json:"arguments_fragment,omitempty"`
}

// UsageSource 标明用量数字的可信来源。
type UsageSource string

const (
	UsageSourceProvider    UsageSource = "provider"    // 供应商回报
	UsageSourceEstimated   UsageSource = "estimated"   // 本地估算
	UsageSourceUnavailable UsageSource = "unavailable" // 不可用
)

// Usage 汇总本轮 token / 费用。
type Usage struct {
	InputTokens       int64       `json:"input_tokens,omitempty"`
	OutputTokens      int64       `json:"output_tokens,omitempty"`
	TotalTokens       int64       `json:"total_tokens,omitempty"`
	CachedInputTokens int64       `json:"cached_input_tokens,omitempty"`
	ReasoningTokens   int64       `json:"reasoning_tokens,omitempty"`
	CostMicros        int64       `json:"cost_micros,omitempty"` // 微美元等约定单位
	Source            UsageSource `json:"source,omitempty"`
}

// Capabilities 描述供应商/模型能力，供上层做能力探测或裁剪。
type Capabilities struct {
	Streaming           bool `json:"streaming"`
	Vision              bool `json:"vision"`
	NativeTools         bool `json:"native_tools"`
	ParallelToolCalls   bool `json:"parallel_tool_calls"`
	StrictToolSchema    bool `json:"strict_tool_schema"`
	StructuredOutput    bool `json:"structured_output"`
	Reasoning           bool `json:"reasoning"`
	PromptCache         bool `json:"prompt_cache"`
	ContextWindowTokens int  `json:"context_window_tokens"`
	MaxOutputTokens     int  `json:"max_output_tokens"`
}
