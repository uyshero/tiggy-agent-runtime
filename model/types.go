// Package model defines provider-neutral model generation contracts.
package model

import (
	"context"
	"encoding/json"

	"github.com/uyshero/tiggy-agent-runtime/message"
)

// DeltaSink is called synchronously in stream order. Returning an error asks
// the provider to stop generation and return that error to the caller.
type DeltaSink func(Delta) error

// Model is the provider-neutral generation contract.
type Model interface {
	Generate(context.Context, Request, DeltaSink) (Response, error)
}

// TokenCounter is an optional capability used by context planning.
type TokenCounter interface {
	CountTokens(context.Context, TokenRequest) (int64, error)
}

type RequestPurpose string

const (
	PurposeAgent      RequestPurpose = "agent"
	PurposeCompaction RequestPurpose = "compaction"
	PurposeMemory     RequestPurpose = "memory"
	PurposeEvaluation RequestPurpose = "evaluation"
)

// Identity captures the model binding used for one request. Credentials stay
// inside the provider implementation and must not be carried in this value.
type Identity struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Revision string `json:"revision,omitempty"`
}

type ToolConcurrencyClass string

const (
	ToolConcurrencySequential ToolConcurrencyClass = "sequential"
	ToolConcurrencySafe       ToolConcurrencyClass = "safe"
)

type ToolDefinition struct {
	Name             string               `json:"name"`
	Description      string               `json:"description,omitempty"`
	InputSchema      json.RawMessage      `json:"input_schema"`
	OutputSchema     json.RawMessage      `json:"output_schema,omitempty"`
	ConcurrencyClass ToolConcurrencyClass `json:"concurrency_class,omitempty"`
}

func (d ToolDefinition) Clone() ToolDefinition {
	cloned := d
	cloned.InputSchema = append(json.RawMessage(nil), d.InputSchema...)
	cloned.OutputSchema = append(json.RawMessage(nil), d.OutputSchema...)
	return cloned
}

type Request struct {
	RunID           string            `json:"run_id"`
	CallID          string            `json:"call_id"`
	Purpose         RequestPurpose    `json:"purpose"`
	Model           Identity          `json:"model"`
	Messages        []message.Message `json:"messages"`
	Tools           []ToolDefinition  `json:"tools,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens"`
	Parameters      json.RawMessage   `json:"parameters,omitempty"`
}

type TokenRequest struct {
	Model    Identity          `json:"model"`
	Messages []message.Message `json:"messages"`
	Tools    []ToolDefinition  `json:"tools,omitempty"`
}

type StopReason string

const (
	StopReasonComplete StopReason = "complete"
	StopReasonToolCall StopReason = "tool_call"
	StopReasonLength   StopReason = "length"
	StopReasonCanceled StopReason = "canceled"
	StopReasonError    StopReason = "error"
)

type Response struct {
	Message           message.Message `json:"message"`
	StopReason        StopReason      `json:"stop_reason"`
	ProviderRequestID string          `json:"provider_request_id,omitempty"`
	Usage             Usage           `json:"usage"`
}

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

type Delta struct {
	Type          DeltaType      `json:"type"`
	Index         int            `json:"index"`
	Text          string         `json:"text,omitempty"`
	ToolCall      *ToolCallDelta `json:"tool_call,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	StopReason    StopReason     `json:"stop_reason,omitempty"`
	ProviderError *ProviderError `json:"provider_error,omitempty"`
}

type ToolCallDelta struct {
	Index             int    `json:"index"`
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	ArgumentsFragment string `json:"arguments_fragment,omitempty"`
}

type UsageSource string

const (
	UsageSourceProvider    UsageSource = "provider"
	UsageSourceEstimated   UsageSource = "estimated"
	UsageSourceUnavailable UsageSource = "unavailable"
)

type Usage struct {
	InputTokens       int64       `json:"input_tokens,omitempty"`
	OutputTokens      int64       `json:"output_tokens,omitempty"`
	TotalTokens       int64       `json:"total_tokens,omitempty"`
	CachedInputTokens int64       `json:"cached_input_tokens,omitempty"`
	ReasoningTokens   int64       `json:"reasoning_tokens,omitempty"`
	CostMicros        int64       `json:"cost_micros,omitempty"`
	Source            UsageSource `json:"source,omitempty"`
}

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
