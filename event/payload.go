package event

import (
	"fmt"

	"github.com/uyshero/tiggy-agent-runtime/model"
)

const ProtocolV1 = "tiggy.runtime.event.v1"

// RunStartedPayload 是 run.started 事件载荷。
type RunStartedPayload struct {
	Agent          string                  `json:"agent"`
	Skills         []ActivatedSkillPayload `json:"skills,omitempty"`
	ToolGroups     []string                `json:"tool_groups,omitempty"`
	ContinuationID string                  `json:"continuation_id,omitempty"`
}

// ActivatedSkillPayload 记录本次 Run 已激活 Skill 的摘要。
type ActivatedSkillPayload struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// RunTerminalPayload 是 Run 终态事件（completed/failed/canceled/suspended）载荷。
type RunTerminalPayload struct {
	Status       string        `json:"status"`
	Reason       string        `json:"reason,omitempty"`
	Iterations   int           `json:"iterations"`
	Usage        model.Usage   `json:"usage"`
	Error        *ErrorPayload `json:"error,omitempty"`
	SuspensionID string        `json:"suspension_id,omitempty"`
}

// ErrorPayload 是可安全展示的错误摘要。
type ErrorPayload struct {
	Class     string `json:"class"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// ModelCallPayload 是模型调用开始/完成事件的公共字段。
type ModelCallPayload struct {
	CallID     string               `json:"call_id"`
	Model      model.Identity       `json:"model"`
	Attempt    int                  `json:"attempt"`
	Context    *ModelContextPayload `json:"context,omitempty"`
	Status     string               `json:"status,omitempty"`
	StopReason string               `json:"stop_reason,omitempty"`
	Usage      model.Usage          `json:"usage,omitempty"`
	Error      *ErrorPayload        `json:"error,omitempty"`
}

// ModelContextPayload is a bounded, content-free manifest of the exact
// Request view associated with model.call.started.
type ModelContextPayload struct {
	ProjectedMessageCount  int   `json:"projected_message_count"`
	RequestMessageCount    int   `json:"request_message_count"`
	SystemMessageCount     int   `json:"system_message_count"`
	UserMessageCount       int   `json:"user_message_count"`
	AssistantMessageCount  int   `json:"assistant_message_count"`
	ToolMessageCount       int   `json:"tool_message_count"`
	PublicMessageCount     int   `json:"public_message_count"`
	InternalMessageCount   int   `json:"internal_message_count"`
	RecalledMemoryMessages int   `json:"recalled_memory_messages"`
	CompressionSummaries   int   `json:"compression_summaries"`
	TextBlocks             int   `json:"text_blocks"`
	TextBytes              int64 `json:"text_bytes"`
	ThinkingBlocks         int   `json:"thinking_blocks"`
	ThinkingBytes          int64 `json:"thinking_bytes"`
	ImageBlocks            int   `json:"image_blocks"`
	ToolCallBlocks         int   `json:"tool_call_blocks"`
	ToolArgumentBytes      int64 `json:"tool_argument_bytes"`
	ToolResultBlocks       int   `json:"tool_result_blocks"`
	ArtifactBlocks         int   `json:"artifact_blocks"`
	ToolDefinitionCount    int   `json:"tool_definition_count"`
	ToolDefinitionBytes    int64 `json:"tool_definition_bytes"`
}

func (p ModelContextPayload) Validate() error {
	counts := []int{
		p.ProjectedMessageCount, p.RequestMessageCount, p.SystemMessageCount, p.UserMessageCount,
		p.AssistantMessageCount, p.ToolMessageCount, p.PublicMessageCount, p.InternalMessageCount,
		p.RecalledMemoryMessages, p.CompressionSummaries, p.TextBlocks, p.ThinkingBlocks,
		p.ImageBlocks, p.ToolCallBlocks, p.ToolResultBlocks, p.ArtifactBlocks, p.ToolDefinitionCount,
	}
	for _, count := range counts {
		if count < 0 {
			return fmt.Errorf("model context counts cannot be negative")
		}
	}
	if p.ProjectedMessageCount < p.RequestMessageCount || p.RequestMessageCount <= 0 {
		return fmt.Errorf("model context projected/request message counts are inconsistent")
	}
	if p.SystemMessageCount+p.UserMessageCount+p.AssistantMessageCount+p.ToolMessageCount != p.RequestMessageCount {
		return fmt.Errorf("model context role counts are inconsistent")
	}
	if p.PublicMessageCount+p.InternalMessageCount != p.RequestMessageCount {
		return fmt.Errorf("model context visibility counts are inconsistent")
	}
	if p.RecalledMemoryMessages+p.CompressionSummaries > p.SystemMessageCount {
		return fmt.Errorf("model context projection counts exceed System Messages")
	}
	blockCount := p.TextBlocks + p.ThinkingBlocks + p.ImageBlocks + p.ToolCallBlocks + p.ToolResultBlocks + p.ArtifactBlocks
	if blockCount < p.RequestMessageCount {
		return fmt.Errorf("model context Content block counts are inconsistent")
	}
	if p.TextBytes < 0 || p.ThinkingBytes < 0 || p.ToolArgumentBytes < 0 || p.ToolDefinitionBytes < 0 {
		return fmt.Errorf("model context byte counts cannot be negative")
	}
	if (p.ToolDefinitionCount == 0) != (p.ToolDefinitionBytes == 0) {
		return fmt.Errorf("model context Tool Definition counts are inconsistent")
	}
	return nil
}

type ModelDeltaPayload struct {
	CallID string      `json:"call_id"`
	Delta  model.Delta `json:"delta"`
}

type ContextPreparePayload struct {
	Model                  model.Identity `json:"model"`
	Status                 string         `json:"status,omitempty"`
	CountSource            string         `json:"count_source,omitempty"`
	ContextWindowTokens    int64          `json:"context_window_tokens,omitempty"`
	InputTokens            int64          `json:"input_tokens,omitempty"`
	OutputReserveTokens    int64          `json:"output_reserve_tokens"`
	SafetyMarginTokens     int64          `json:"safety_margin_tokens,omitempty"`
	RequiredTokens         int64          `json:"required_tokens,omitempty"`
	AvailableInputTokens   int64          `json:"available_input_tokens,omitempty"`
	RemainingInputTokens   int64          `json:"remaining_input_tokens,omitempty"`
	OverflowTokens         int64          `json:"overflow_tokens,omitempty"`
	UtilizationBasisPoints int64          `json:"utilization_basis_points,omitempty"`
	Pressure               string         `json:"pressure,omitempty"`
	Fits                   bool           `json:"fits,omitempty"`
	Error                  *ErrorPayload  `json:"error,omitempty"`
}

type CompressionPayload struct {
	Model             model.Identity `json:"model"`
	Trigger           string         `json:"trigger,omitempty"`
	Status            string         `json:"status,omitempty"`
	StrategyName      string         `json:"strategy_name,omitempty"`
	StrategyVersion   string         `json:"strategy_version,omitempty"`
	BeforeInputTokens int64          `json:"before_input_tokens"`
	AfterInputTokens  int64          `json:"after_input_tokens,omitempty"`
	TargetInputTokens int64          `json:"target_input_tokens"`
	OverflowTokens    int64          `json:"overflow_tokens"`
	CandidateGroups   int            `json:"candidate_groups"`
	CoveredGroups     int            `json:"covered_groups,omitempty"`
	CoveredMessageIDs []string       `json:"covered_message_ids,omitempty"`
	SummaryMessageID  string         `json:"summary_message_id,omitempty"`
	Fallback          string         `json:"fallback,omitempty"`
	Error             *ErrorPayload  `json:"error,omitempty"`
}

type RetryScheduledPayload struct {
	FailedCallID string         `json:"failed_call_id"`
	Model        model.Identity `json:"model"`
	NextAttempt  int            `json:"next_attempt"`
	DelayMillis  int64          `json:"delay_millis"`
	Reason       string         `json:"reason"`
	Error        *ErrorPayload  `json:"error,omitempty"`
}

type FallbackActivatedPayload struct {
	FailedCallID  string         `json:"failed_call_id"`
	From          model.Identity `json:"from"`
	To            model.Identity `json:"to"`
	FallbackIndex int            `json:"fallback_index"`
	Reason        string         `json:"reason"`
	Error         *ErrorPayload  `json:"error,omitempty"`
}

type ToolCallPayload struct {
	CallID    string        `json:"call_id"`
	Name      string        `json:"name"`
	Status    string        `json:"status,omitempty"`
	IsError   bool          `json:"is_error,omitempty"`
	Retryable bool          `json:"retryable,omitempty"`
	Error     *ErrorPayload `json:"error,omitempty"`
}

// ApprovalPayload intentionally excludes raw Tool arguments. ArgumentsDigest
// binds the Event to the exact call retained in Agent State.
type ApprovalPayload struct {
	ApprovalID      string `json:"approval_id"`
	SuspensionID    string `json:"suspension_id"`
	CallID          string `json:"call_id"`
	Tool            string `json:"tool"`
	ArgumentsDigest string `json:"arguments_digest"`
	ToolDigest      string `json:"tool_digest"`
	Status          string `json:"status,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type ToolResultOffloadPayload struct {
	CallID      string        `json:"call_id"`
	Name        string        `json:"name"`
	Status      string        `json:"status,omitempty"`
	SourceBytes int64         `json:"source_bytes"`
	MediaType   string        `json:"media_type"`
	Digest      string        `json:"digest"`
	ObjectRefID string        `json:"object_ref_id,omitempty"`
	Fallback    string        `json:"fallback,omitempty"`
	Error       *ErrorPayload `json:"error,omitempty"`
}

// MemoryPayload reports bounded operation metadata without exposing recalled
// text or the application-defined Scope key.
type MemoryPayload struct {
	Status           string        `json:"status,omitempty"`
	Namespace        string        `json:"namespace"`
	Limit            int           `json:"limit,omitempty"`
	ItemCount        int           `json:"item_count,omitempty"`
	ExpiredItemCount int           `json:"expired_item_count,omitempty"`
	TotalBytes       int64         `json:"total_bytes,omitempty"`
	MessageCount     int           `json:"message_count,omitempty"`
	ObservationBytes int64         `json:"observation_bytes,omitempty"`
	FailureMode      string        `json:"failure_mode"`
	Error            *ErrorPayload `json:"error,omitempty"`
}
