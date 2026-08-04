package record

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// SanitizedEvent 只允许替换 Payload。
// Recorder 会校验协议信封与源 Event 保持一致。
type SanitizedEvent struct {
	Event           event.Event
	PayloadRedacted bool
}

func (s SanitizedEvent) Validate(source event.Event) error {
	if s.Event.ProtocolVersion != source.ProtocolVersion || s.Event.ID != source.ID ||
		s.Event.RunID != source.RunID || s.Event.ParentID != source.ParentID ||
		s.Event.Type != source.Type || s.Event.Sequence != source.Sequence ||
		!s.Event.CreatedAt.Equal(source.CreatedAt) {
		return fmt.Errorf("recording Sanitizer may change only Event payload")
	}
	if err := s.Event.Validate(); err != nil {
		return fmt.Errorf("sanitized Event: %w", err)
	}
	if !bytes.Equal(s.Event.Payload, source.Payload) && !s.PayloadRedacted {
		return fmt.Errorf("recording Sanitizer changed payload without marking it redacted")
	}
	return nil
}

// Sanitizer 在账本持久化前移除或转换 payload。
// 实现必须并发安全，且不得修改 Event 信封字段；默认使用 SafeSanitizer。
type Sanitizer interface {
	Sanitize(event.Event) (SanitizedEvent, error)
}

// SanitizerFunc 将函数适配为 Sanitizer。
type SanitizerFunc func(event.Event) (SanitizedEvent, error)

func (f SanitizerFunc) Sanitize(value event.Event) (SanitizedEvent, error) {
	return f(cloneEvent(value))
}

// SafeSanitizer 保留已审阅的生命周期元数据，移除 Model Delta 文本/推理/Tool 片段与供应商错误，
// 并对未知 Event 类型丢弃 payload，直至其隐私契约被审阅。
type SafeSanitizer struct{}

func (SafeSanitizer) Sanitize(value event.Event) (SanitizedEvent, error) {
	value = cloneEvent(value)
	switch value.Type {
	case event.ModelCallDelta:
		var payload event.ModelDeltaPayload
		if err := json.Unmarshal(value.Payload, &payload); err != nil {
			return SanitizedEvent{}, fmt.Errorf("decode Model Delta payload: %w", err)
		}
		delta := model.Delta{Type: payload.Delta.Type, Index: payload.Delta.Index, StopReason: payload.Delta.StopReason}
		if payload.Delta.Usage != nil {
			usage := *payload.Delta.Usage
			delta.Usage = &usage
		}
		safePayload := event.ModelDeltaPayload{CallID: payload.CallID, Delta: delta}
		encoded, err := json.Marshal(safePayload)
		if err != nil {
			return SanitizedEvent{}, fmt.Errorf("encode sanitized Model Delta: %w", err)
		}
		value.Payload = encoded
		return SanitizedEvent{Event: value, PayloadRedacted: true}, nil
	case event.ToolCallDelta:
		value.Payload = nil
		return SanitizedEvent{Event: value, PayloadRedacted: true}, nil
	default:
		if knownSafeEventType(value.Type) {
			return SanitizedEvent{Event: value}, nil
		}
		redacted := len(value.Payload) > 0
		value.Payload = nil
		return SanitizedEvent{Event: value, PayloadRedacted: redacted}, nil
	}
}

// FullSanitizer retains every payload byte. It must be selected explicitly;
// callers then own content classification, encryption, access, and retention.
type FullSanitizer struct{}

func (FullSanitizer) Sanitize(value event.Event) (SanitizedEvent, error) {
	return SanitizedEvent{Event: cloneEvent(value)}, nil
}

func knownSafeEventType(value event.Type) bool {
	switch value {
	case event.RunStarted, event.RunCompleted, event.RunFailed, event.RunCanceled, event.RunSuspended,
		event.ApprovalRequired, event.ApprovalResolved,
		event.ModelCallStarted, event.ModelCallCompleted,
		event.ToolCallStarted, event.ToolCallCompleted,
		event.ToolResultOffloadStarted, event.ToolResultOffloadCompleted, event.ToolResultOffloadFailed,
		event.ContextPrepareStarted, event.ContextPrepared,
		event.CompressionStarted, event.CompressionCompleted, event.CompressionFailed,
		event.MemoryRecallStarted, event.MemoryRecallCompleted,
		event.MemoryObserveStarted, event.MemoryObserveCompleted, event.MemoryObserveFailed,
		event.RetryScheduled, event.FallbackActivated, event.LimitReached, event.RuntimeError:
		return true
	default:
		return false
	}
}
