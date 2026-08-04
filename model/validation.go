package model

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/message"
)

// Validate 校验一次模型生成请求的标识、用途、消息与工具定义。
func (r Request) Validate() error {
	if strings.TrimSpace(r.RunID) == "" || strings.TrimSpace(r.CallID) == "" {
		return fmt.Errorf("run id and call id are required")
	}
	switch r.Purpose {
	case PurposeAgent, PurposeCompaction, PurposeMemory, PurposeEvaluation:
	default:
		return fmt.Errorf("unsupported request purpose %q", r.Purpose)
	}
	if err := r.Model.Validate(); err != nil {
		return err
	}
	if r.MaxOutputTokens <= 0 {
		return fmt.Errorf("max output tokens must be positive")
	}
	if len(r.Messages) == 0 {
		return fmt.Errorf("request messages are required")
	}
	for index, value := range r.Messages {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
	}
	for index, tool := range r.Tools {
		if err := tool.Validate(); err != nil {
			return fmt.Errorf("tool %d: %w", index, err)
		}
	}
	if len(r.Parameters) > 0 && !json.Valid(r.Parameters) {
		return fmt.Errorf("model parameters must be valid JSON")
	}
	return nil
}

func (d ToolDefinition) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("tool name is required")
	}
	if len(d.InputSchema) == 0 || !json.Valid(d.InputSchema) {
		return fmt.Errorf("tool %q input schema must be valid JSON", d.Name)
	}
	if len(d.OutputSchema) > 0 && !json.Valid(d.OutputSchema) {
		return fmt.Errorf("tool %q output schema must be valid JSON", d.Name)
	}
	switch d.ConcurrencyClass {
	case "", ToolConcurrencySequential, ToolConcurrencySafe:
	default:
		return fmt.Errorf("tool %q has unsupported concurrency class %q", d.Name, d.ConcurrencyClass)
	}
	return nil
}

func (i Identity) Validate() error {
	if strings.TrimSpace(i.Provider) == "" || strings.TrimSpace(i.Model) == "" {
		return fmt.Errorf("model provider and name are required")
	}
	return nil
}

func (u Usage) Validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 || u.CachedInputTokens < 0 || u.ReasoningTokens < 0 || u.CostMicros < 0 {
		return fmt.Errorf("usage values cannot be negative")
	}
	if u.TotalTokens > 0 && u.TotalTokens < u.InputTokens+u.OutputTokens {
		return fmt.Errorf("total tokens cannot be less than input plus output tokens")
	}
	switch u.Source {
	case "", UsageSourceProvider, UsageSourceEstimated, UsageSourceUnavailable:
	default:
		return fmt.Errorf("unsupported usage source %q", u.Source)
	}
	return nil
}

func (u Usage) Add(other Usage) Usage {
	source := u.Source
	if source == "" {
		source = other.Source
	} else if other.Source != "" && source != other.Source {
		source = UsageSourceEstimated
	}
	return Usage{
		InputTokens:       u.InputTokens + other.InputTokens,
		OutputTokens:      u.OutputTokens + other.OutputTokens,
		TotalTokens:       u.TotalTokens + other.TotalTokens,
		CachedInputTokens: u.CachedInputTokens + other.CachedInputTokens,
		ReasoningTokens:   u.ReasoningTokens + other.ReasoningTokens,
		CostMicros:        u.CostMicros + other.CostMicros,
		Source:            source,
	}
}

func (r Response) Validate() error {
	if err := r.Message.Validate(); err != nil {
		return fmt.Errorf("response message: %w", err)
	}
	if r.Message.Role != message.RoleAssistant {
		return fmt.Errorf("response message role must be assistant")
	}
	switch r.StopReason {
	case StopReasonComplete, StopReasonToolCall, StopReasonLength, StopReasonCanceled, StopReasonError:
	default:
		return fmt.Errorf("unsupported stop reason %q", r.StopReason)
	}
	if err := r.Usage.Validate(); err != nil {
		return fmt.Errorf("response usage: %w", err)
	}
	return nil
}
