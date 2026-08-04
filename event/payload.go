package event

import (
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
	CallID     string         `json:"call_id"`
	Model      model.Identity `json:"model"`
	Attempt    int            `json:"attempt"`
	Status     string         `json:"status,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
	Usage      model.Usage    `json:"usage,omitempty"`
	Error      *ErrorPayload  `json:"error,omitempty"`
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
