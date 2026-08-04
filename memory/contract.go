// Package memory 定义作用域内的长期回忆（Recall）与观察（Observe）契约。
//
// Core 通过 Port 取证并在一轮结束后投喂观察；实现方不得把企业租户等类型泄漏进公共 API。
// 召回结果默认视为不可信，须经校验后再投影进模型上下文。
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uyshero/tiggy-agent-runtime/message"
)

const (
	DefaultRecallLimit         = 16        // 单次召回条数默认上限
	DefaultMaxItemBytes        = 8 << 10   // 单条 Item 文本默认上限（8 KiB）
	DefaultMaxRecallBytes      = 64 << 10  // 单次召回总文本默认上限（64 KiB）
	DefaultMaxObservationBytes = 256 << 10 // 单次观察序列化体积默认上限（256 KiB）
)

// FailureMode 控制 Port 普通错误如何影响 Agent Run。
type FailureMode string

const (
	FailureModeDegrade FailureMode = "degrade"  // 降级：跳过 Memory，继续 Run
	FailureModeFailRun FailureMode = "fail_run" // 失败即终止 Run
)

// Scope 是应用定义的隔离边界，不含企业级租户类型。
type Scope struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

// Validate 要求 Namespace/Key 为非空、已 trim、合法 UTF-8 的有界字符串。
func (s Scope) Validate() error {
	if !canonicalBounded(s.Namespace, 128) || !canonicalBounded(s.Key, 512) {
		return fmt.Errorf("memory scope namespace and key are required canonical values")
	}
	return nil
}

// Provenance 标识记录的应用侧来源类别，以及可选的 Run/消息溯源。
type Provenance struct {
	Source     string   `json:"source"`
	RunID      string   `json:"run_id,omitempty"`
	MessageIDs []string `json:"message_ids,omitempty"`
}

// Validate 校验来源与溯源字段的一致性（有 MessageIDs 时必须带 RunID）。
func (p Provenance) Validate() error {
	if !canonicalBounded(p.Source, 256) {
		return fmt.Errorf("memory provenance source is required")
	}
	if p.RunID != "" && !canonicalBounded(p.RunID, 512) {
		return fmt.Errorf("memory provenance run id is invalid")
	}
	if len(p.MessageIDs) > 64 {
		return fmt.Errorf("memory provenance has too many message ids")
	}
	if len(p.MessageIDs) > 0 && p.RunID == "" {
		return fmt.Errorf("memory provenance message ids require a run id")
	}
	seen := make(map[string]struct{}, len(p.MessageIDs))
	for _, id := range p.MessageIDs {
		if !canonicalBounded(id, 512) {
			return fmt.Errorf("memory provenance message id is invalid")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate memory provenance message id %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (p Provenance) clone() Provenance {
	p.MessageIDs = append([]string(nil), p.MessageIDs...)
	return p
}

// Item 是返回给模型上下文的一条作用域内文本记忆。
type Item struct {
	ID         string     `json:"id"`
	Scope      Scope      `json:"scope"`
	Text       string     `json:"text"`
	Provenance Provenance `json:"provenance"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// Validate 校验 Item 字段；ExpiresAt 若存在必须是非零 UTC 时间。
func (i Item) Validate() error {
	if !canonicalBounded(i.ID, 512) {
		return fmt.Errorf("memory item id is required")
	}
	if err := i.Scope.Validate(); err != nil {
		return err
	}
	if err := i.Provenance.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(i.Text) == "" || !utf8.ValidString(i.Text) {
		return fmt.Errorf("memory item text must be non-empty UTF-8")
	}
	if i.ExpiresAt != nil && (i.ExpiresAt.IsZero() || i.ExpiresAt.Location() != time.UTC) {
		return fmt.Errorf("memory item expiry must be a non-zero UTC timestamp")
	}
	return nil
}

// Clone 返回不共享可变 Provenance / ExpiresAt 的 Item 副本。
func (i Item) Clone() Item {
	i.Provenance = i.Provenance.clone()
	if i.ExpiresAt != nil {
		expiresAt := *i.ExpiresAt
		i.ExpiresAt = &expiresAt
	}
	return i
}

// Expired 报告在给定时刻该 Item 是否已不可用。
// 过期是包含边界：当 at >= ExpiresAt 时视为过期。
func (i Item) Expired(at time.Time) bool {
	return i.ExpiresAt != nil && !at.Before(*i.ExpiresAt)
}

func cloneItems(items []Item) []Item {
	if items == nil {
		return nil
	}
	cloned := make([]Item, len(items))
	for index, item := range items {
		cloned[index] = item.Clone()
	}
	return cloned
}

// RecallRequest 按当前 User 输入请求相关证据。
type RecallRequest struct {
	RunID string          `json:"run_id"`
	Scope Scope           `json:"scope"`
	Input message.Message `json:"input"`
	Limit int             `json:"limit"`
}

// Clone 深拷贝 Input，避免调用方与 Port 共享 Message 缓冲。
func (r RecallRequest) Clone() RecallRequest {
	r.Input = message.Clone(r.Input)
	return r
}

// Validate 要求 Input 为 User 角色，且 Limit 为正。
func (r RecallRequest) Validate() error {
	if !canonicalBounded(r.RunID, 512) {
		return fmt.Errorf("memory recall run id is required")
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if err := r.Input.Validate(); err != nil || r.Input.Role != message.RoleUser {
		if err == nil {
			err = fmt.Errorf("memory recall input must have user role")
		}
		return err
	}
	if r.Limit <= 0 {
		return fmt.Errorf("memory recall limit must be positive")
	}
	return nil
}

// RecallResult 包含不可信的 Items；Core 在投影前必须再校验。
type RecallResult struct {
	Items []Item `json:"items"`
}

// Clone 返回 Items 的深拷贝。
func (r RecallResult) Clone() RecallResult {
	return RecallResult{Items: cloneItems(r.Items)}
}

// Validate 按请求与 Options 校验条数、Scope、去重与字节上限。
func (r RecallResult) Validate(request RecallRequest, options Options) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if len(r.Items) > request.Limit || len(r.Items) > options.RecallLimit {
		return fmt.Errorf("memory recall returned too many items")
	}
	seen := make(map[string]struct{}, len(r.Items))
	total := 0
	for index, item := range r.Items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("memory item %d: %w", index, err)
		}
		if item.Scope != request.Scope {
			return fmt.Errorf("memory item %d crossed the requested scope", index)
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("duplicate memory item id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if len(item.Text) > options.MaxItemBytes {
			return fmt.Errorf("memory item %d exceeds the item limit", index)
		}
		total += len(item.Text)
		if total > options.MaxRecallBytes {
			return fmt.Errorf("memory recall exceeds the total byte limit")
		}
	}
	return nil
}

// ObserveRequest 仅包含一轮已完成 Agent Turn 中产生的消息。
type ObserveRequest struct {
	RunID    string            `json:"run_id"`
	Scope    Scope             `json:"scope"`
	Messages []message.Message `json:"messages"`
}

// Clone 深拷贝 Messages。
func (r ObserveRequest) Clone() ObserveRequest {
	r.Messages = message.CloneAll(r.Messages)
	return r
}

// Validate 要求至少两条非 System 消息，并限制序列化体积。
func (r ObserveRequest) Validate(options Options) error {
	if !canonicalBounded(r.RunID, 512) {
		return fmt.Errorf("memory observe run id is required")
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if len(r.Messages) < 2 {
		return fmt.Errorf("memory observation requires a completed turn")
	}
	seen := make(map[string]struct{}, len(r.Messages))
	for index, value := range r.Messages {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("memory observation message %d: %w", index, err)
		}
		if value.Role == message.RoleSystem {
			return fmt.Errorf("memory observation cannot contain System messages")
		}
		if _, exists := seen[value.ID]; exists {
			return fmt.Errorf("duplicate memory observation message id %q", value.ID)
		}
		seen[value.ID] = struct{}{}
	}
	encoded, err := json.Marshal(r.Messages)
	if err != nil {
		return err
	}
	if len(encoded) > options.MaxObservationBytes {
		return fmt.Errorf("memory observation exceeds the byte limit")
	}
	return nil
}

// Port 按 Scope 取证，并观察一轮已完成的 Agent Turn。
// Agent 被并发复用时，实现必须并发安全，并尊重 Context 取消。
type Port interface {
	Recall(context.Context, RecallRequest) (RecallResult, error)
	Observe(context.Context, ObserveRequest) error
}

// Options 限制穿越 Port 的数据量，并选择普通错误的处理策略。
// 零值字段由 NormalizeOptions 填入 DefaultOptions。
type Options struct {
	RecallLimit         int         `json:"recall_limit"`
	MaxItemBytes        int         `json:"max_item_bytes"`
	MaxRecallBytes      int         `json:"max_recall_bytes"`
	MaxObservationBytes int         `json:"max_observation_bytes"`
	RecallFailureMode   FailureMode `json:"recall_failure_mode"`
	ObserveFailureMode  FailureMode `json:"observe_failure_mode"`
}

// DefaultOptions 返回标准的有界、默认可降级 Memory 策略。
func DefaultOptions() Options {
	return Options{
		RecallLimit: DefaultRecallLimit, MaxItemBytes: DefaultMaxItemBytes,
		MaxRecallBytes: DefaultMaxRecallBytes, MaxObservationBytes: DefaultMaxObservationBytes,
		RecallFailureMode: FailureModeDegrade, ObserveFailureMode: FailureModeDegrade,
	}
}

// NormalizeOptions 填充零值字段，并校验全部边界配置。
func NormalizeOptions(options Options) (Options, error) {
	defaults := DefaultOptions()
	if options.RecallLimit == 0 {
		options.RecallLimit = defaults.RecallLimit
	}
	if options.MaxItemBytes == 0 {
		options.MaxItemBytes = defaults.MaxItemBytes
	}
	if options.MaxRecallBytes == 0 {
		options.MaxRecallBytes = defaults.MaxRecallBytes
	}
	if options.MaxObservationBytes == 0 {
		options.MaxObservationBytes = defaults.MaxObservationBytes
	}
	if options.RecallFailureMode == "" {
		options.RecallFailureMode = defaults.RecallFailureMode
	}
	if options.ObserveFailureMode == "" {
		options.ObserveFailureMode = defaults.ObserveFailureMode
	}
	if options.RecallLimit <= 0 || options.MaxItemBytes <= 0 || options.MaxRecallBytes < options.MaxItemBytes || options.MaxObservationBytes <= 0 {
		return Options{}, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("memory limits must be positive and total recall must cover one item")}
	}
	if !validFailureMode(options.RecallFailureMode) || !validFailureMode(options.ObserveFailureMode) {
		return Options{}, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("memory failure mode must be degrade or fail_run")}
	}
	return options, nil
}

// canonicalBounded 要求非空、无首尾空白、合法 UTF-8，且字节长度不超过 maximum。
func canonicalBounded(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && len(value) <= maximum
}

func validFailureMode(value FailureMode) bool {
	return value == FailureModeDegrade || value == FailureModeFailRun
}
