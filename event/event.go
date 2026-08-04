// Package event 定义版本化的 Agent Runtime 生命周期协议。
package event

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Type 标识事件种类；载荷 schema 按 Type 与 ProtocolVersion 版本化。
type Type string

// Sink 按序号同步消费事件；返回错误会中止 Run。
// Sink 必须快速返回，或自行提供有界 I/O。
type Sink func(Event) error

const (
	RunStarted                 Type = "run.started"
	RunCompleted               Type = "run.completed"
	RunFailed                  Type = "run.failed"
	RunCanceled                Type = "run.canceled"
	RunSuspended               Type = "run.suspended"
	ApprovalRequired           Type = "approval.required"
	ApprovalResolved           Type = "approval.resolved"
	ModelCallStarted           Type = "model.call.started"
	ModelCallDelta             Type = "model.call.delta"
	ModelCallCompleted         Type = "model.call.completed"
	ToolCallStarted            Type = "tool.call.started"
	ToolCallDelta              Type = "tool.call.delta"
	ToolCallCompleted          Type = "tool.call.completed"
	ToolResultOffloadStarted   Type = "tool.result.offload.started"
	ToolResultOffloadCompleted Type = "tool.result.offload.completed"
	ToolResultOffloadFailed    Type = "tool.result.offload.failed"
	ContextPrepareStarted      Type = "context.prepare.started"
	ContextPrepared            Type = "context.prepared"
	CompressionStarted         Type = "context.compression.started"
	CompressionCompleted       Type = "context.compression.completed"
	CompressionFailed          Type = "context.compression.failed"
	MemoryRecallStarted        Type = "memory.recall.started"
	MemoryRecallCompleted      Type = "memory.recall.completed"
	MemoryObserveStarted       Type = "memory.observe.started"
	MemoryObserveCompleted     Type = "memory.observe.completed"
	MemoryObserveFailed        Type = "memory.observe.failed"
	RetryScheduled             Type = "retry.scheduled"
	FallbackActivated          Type = "fallback.activated"
	LimitReached               Type = "limit.reached"
	RuntimeError               Type = "runtime.error"
)

// Event 是稳定的生命周期信封；载荷 schema 由 Type/ProtocolVersion 版本化，而非 Go any。
type Event struct {
	ProtocolVersion string          `json:"protocol_version"`
	ID              string          `json:"id"`
	RunID           string          `json:"run_id"`
	ParentID        string          `json:"parent_id,omitempty"`
	Type            Type            `json:"type"`
	Sequence        int64           `json:"sequence"`
	CreatedAt       time.Time       `json:"created_at"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// Validate 检查信封必填字段与 Payload JSON 合法性。
func (e Event) Validate() error {
	if strings.TrimSpace(e.ProtocolVersion) == "" || strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.RunID) == "" {
		return fmt.Errorf("protocol version, event id, and run id are required")
	}
	if e.Type == "" {
		return fmt.Errorf("event type is required")
	}
	if e.Sequence <= 0 {
		return fmt.Errorf("event sequence must be positive")
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("event timestamp is required")
	}
	if len(e.Payload) > 0 && !json.Valid(e.Payload) {
		return fmt.Errorf("event payload must be valid JSON")
	}
	return nil
}

func (e Event) Terminal() bool {
	return e.Type == RunCompleted || e.Type == RunFailed || e.Type == RunCanceled || e.Type == RunSuspended
}
