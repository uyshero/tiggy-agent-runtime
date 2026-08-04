package agenttest

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

type Trace struct {
	mu     sync.Mutex
	events []event.Event
}

func (t *Trace) Sink(value event.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, cloneEvent(value))
	return nil
}

func (t *Trace) Events() []event.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	values := make([]event.Event, len(t.events))
	for index, value := range t.events {
		values[index] = cloneEvent(value)
	}
	return values
}

func ValidateTrace(events []event.Event) error {
	if len(events) < 2 {
		return fmt.Errorf("trace requires at least run start and terminal events")
	}
	runID := events[0].RunID
	ids := make(map[string]event.Type, len(events))
	type operation struct {
		typeValue        event.Type
		callID           string
		name             string
		model            model.Identity
		attempt          int
		status           string
		sourceBytes      int64
		mediaType        string
		digest           string
		namespace        string
		failureMode      string
		limit            int
		messageCount     int
		observationBytes int64
	}
	open := make(map[string]operation)
	completedModels := make(map[string]operation)
	terminals := 0
	runStarts := 0
	for index, value := range events {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("event %d: %w", index, err)
		}
		if value.RunID != runID {
			return fmt.Errorf("event %d changed run id", index)
		}
		if value.Sequence != int64(index+1) {
			return fmt.Errorf("event %d has sequence %d", index, value.Sequence)
		}
		if _, exists := ids[value.ID]; exists {
			return fmt.Errorf("duplicate event id %q", value.ID)
		}
		ids[value.ID] = value.Type
		switch value.Type {
		case event.RunStarted:
			runStarts++
		case event.ModelCallStarted:
			payload, err := DecodePayload[event.ModelCallPayload](value)
			if err != nil || payload.CallID == "" {
				return fmt.Errorf("model start %q has invalid payload", value.ID)
			}
			open[value.ID] = operation{
				typeValue: value.Type, callID: payload.CallID, model: payload.Model, attempt: payload.Attempt,
			}
		case event.ContextPrepareStarted:
			payload, err := DecodePayload[event.ContextPreparePayload](value)
			if err != nil || payload.Model.Validate() != nil || payload.OutputReserveTokens <= 0 {
				return fmt.Errorf("context prepare start %q has invalid payload", value.ID)
			}
			open[value.ID] = operation{typeValue: value.Type, model: payload.Model}
		case event.CompressionStarted:
			payload, err := DecodePayload[event.CompressionPayload](value)
			if err != nil || payload.Model.Validate() != nil || payload.BeforeInputTokens <= 0 || payload.TargetInputTokens <= 0 || payload.OverflowTokens <= 0 || payload.CandidateGroups < 0 {
				return fmt.Errorf("compression start %q has invalid payload", value.ID)
			}
			open[value.ID] = operation{typeValue: value.Type, model: payload.Model}
		case event.ToolCallStarted:
			payload, err := DecodePayload[event.ToolCallPayload](value)
			if err != nil || payload.CallID == "" {
				return fmt.Errorf("tool start %q has invalid payload", value.ID)
			}
			open[value.ID] = operation{typeValue: value.Type, callID: payload.CallID, name: payload.Name}
		case event.ToolResultOffloadStarted:
			payload, err := DecodePayload[event.ToolResultOffloadPayload](value)
			toolOperation, parentExists := open[value.ParentID]
			if err != nil || payload.CallID == "" || payload.Name == "" || payload.SourceBytes <= 0 || payload.MediaType == "" || payload.Digest == "" ||
				!parentExists || toolOperation.typeValue != event.ToolCallStarted || toolOperation.callID != payload.CallID || toolOperation.name != payload.Name {
				return fmt.Errorf("tool result offload start %q has invalid payload or parent", value.ID)
			}
			open[value.ID] = operation{
				typeValue: value.Type, callID: payload.CallID, name: payload.Name,
				sourceBytes: payload.SourceBytes, mediaType: payload.MediaType, digest: payload.Digest,
			}
		case event.MemoryRecallStarted:
			payload, err := DecodePayload[event.MemoryPayload](value)
			if err != nil || payload.Namespace == "" || payload.Limit <= 0 || !validMemoryFailureMode(payload.FailureMode) || payload.Status != "" || payload.Error != nil {
				return fmt.Errorf("memory recall start %q has invalid payload", value.ID)
			}
			open[value.ID] = operation{
				typeValue: value.Type, namespace: payload.Namespace,
				failureMode: payload.FailureMode, limit: payload.Limit,
			}
		case event.MemoryObserveStarted:
			payload, err := DecodePayload[event.MemoryPayload](value)
			if err != nil || payload.Namespace == "" || payload.MessageCount < 2 || payload.ObservationBytes <= 0 || !validMemoryFailureMode(payload.FailureMode) || payload.Status != "" || payload.Error != nil {
				return fmt.Errorf("memory observe start %q has invalid payload", value.ID)
			}
			open[value.ID] = operation{
				typeValue: value.Type, namespace: payload.Namespace, failureMode: payload.FailureMode,
				messageCount: payload.MessageCount, observationBytes: payload.ObservationBytes,
			}
		case event.ModelCallCompleted:
			started, exists := open[value.ParentID]
			payload, err := DecodePayload[event.ModelCallPayload](value)
			if !exists || started.typeValue != event.ModelCallStarted || err != nil || payload.CallID != started.callID || payload.Model != started.model || payload.Attempt != started.attempt {
				return fmt.Errorf("model completion %q has no matching start", value.ID)
			}
			delete(open, value.ParentID)
			started.status = payload.Status
			completedModels[value.ID] = started
		case event.ToolCallCompleted:
			started, exists := open[value.ParentID]
			payload, err := DecodePayload[event.ToolCallPayload](value)
			if !exists || started.typeValue != event.ToolCallStarted || err != nil || payload.CallID != started.callID || payload.Name != started.name {
				return fmt.Errorf("tool completion %q has no matching start", value.ID)
			}
			delete(open, value.ParentID)
		case event.ToolResultOffloadCompleted, event.ToolResultOffloadFailed:
			started, exists := open[value.ParentID]
			payload, err := DecodePayload[event.ToolResultOffloadPayload](value)
			if !exists || started.typeValue != event.ToolResultOffloadStarted || err != nil ||
				payload.CallID != started.callID || payload.Name != started.name || payload.SourceBytes != started.sourceBytes ||
				payload.MediaType != started.mediaType || payload.Digest != started.digest || !validToolResultOffloadStatus(value.Type, payload) {
				return fmt.Errorf("tool result offload outcome %q has no matching start", value.ID)
			}
			delete(open, value.ParentID)
		case event.MemoryRecallCompleted:
			started, exists := open[value.ParentID]
			payload, err := DecodePayload[event.MemoryPayload](value)
			if !exists || started.typeValue != event.MemoryRecallStarted || err != nil ||
				payload.Namespace != started.namespace || payload.FailureMode != started.failureMode || payload.Limit != started.limit ||
				!validMemoryRecallStatus(payload) {
				return fmt.Errorf("memory recall outcome %q has no matching start", value.ID)
			}
			delete(open, value.ParentID)
		case event.MemoryObserveCompleted, event.MemoryObserveFailed:
			started, exists := open[value.ParentID]
			payload, err := DecodePayload[event.MemoryPayload](value)
			if !exists || started.typeValue != event.MemoryObserveStarted || err != nil ||
				payload.Namespace != started.namespace || payload.FailureMode != started.failureMode ||
				payload.MessageCount != started.messageCount || payload.ObservationBytes != started.observationBytes ||
				!validMemoryObserveStatus(value.Type, payload) {
				return fmt.Errorf("memory observe outcome %q has no matching start", value.ID)
			}
			delete(open, value.ParentID)
		case event.ContextPrepared:
			started, exists := open[value.ParentID]
			payload, err := DecodePayload[event.ContextPreparePayload](value)
			if !exists || started.typeValue != event.ContextPrepareStarted || err != nil || payload.Model != started.model || !validContextPrepareStatus(payload.Status) {
				return fmt.Errorf("context preparation %q has no matching start", value.ID)
			}
			delete(open, value.ParentID)
		case event.CompressionCompleted, event.CompressionFailed:
			started, exists := open[value.ParentID]
			payload, err := DecodePayload[event.CompressionPayload](value)
			if !exists || started.typeValue != event.CompressionStarted || err != nil || payload.Model != started.model || !validCompressionStatus(value.Type, payload) {
				return fmt.Errorf("compression outcome %q has no matching start", value.ID)
			}
			delete(open, value.ParentID)
		case event.RetryScheduled:
			payload, err := DecodePayload[event.RetryScheduledPayload](value)
			completed := completedModels[value.ParentID]
			if err != nil || payload.FailedCallID == "" || completed.callID != payload.FailedCallID || completed.status != "failed" || payload.Model != completed.model || payload.NextAttempt <= completed.attempt || payload.DelayMillis < 0 {
				return fmt.Errorf("retry event %q has no matching failed model call", value.ID)
			}
		case event.FallbackActivated:
			payload, err := DecodePayload[event.FallbackActivatedPayload](value)
			completed := completedModels[value.ParentID]
			if err != nil || payload.FailedCallID == "" || completed.callID != payload.FailedCallID || completed.status != "failed" || payload.From != completed.model || payload.FallbackIndex <= 0 {
				return fmt.Errorf("fallback event %q has no matching failed model call", value.ID)
			}
		}
		if value.Terminal() {
			terminals++
			payload, err := DecodePayload[event.RunTerminalPayload](value)
			if err != nil || !terminalMatchesStatus(value.Type, payload.Status) {
				return fmt.Errorf("terminal event %q has inconsistent status", value.ID)
			}
			if index != len(events)-1 {
				return fmt.Errorf("terminal event must be last")
			}
		}
	}
	if events[0].Type != event.RunStarted {
		return fmt.Errorf("first event is %q, want %q", events[0].Type, event.RunStarted)
	}
	if runStarts != 1 {
		return fmt.Errorf("run start count = %d, want 1", runStarts)
	}
	if terminals != 1 {
		return fmt.Errorf("terminal event count = %d, want 1", terminals)
	}
	if len(open) != 0 {
		return fmt.Errorf("trace has %d incomplete operations", len(open))
	}
	return nil
}

func validMemoryFailureMode(value string) bool {
	return value == "degrade" || value == "fail_run"
}

func validMemoryRecallStatus(payload event.MemoryPayload) bool {
	switch payload.Status {
	case "completed":
		return payload.Error == nil && payload.ItemCount > 0 && payload.TotalBytes > 0 &&
			payload.ExpiredItemCount >= 0 && payload.ItemCount+payload.ExpiredItemCount <= payload.Limit
	case "empty":
		return payload.Error == nil && payload.ItemCount == 0 && payload.TotalBytes == 0 &&
			payload.ExpiredItemCount >= 0 && payload.ExpiredItemCount <= payload.Limit
	case "degraded", "failed", "canceled":
		return payload.Error != nil && payload.ItemCount == 0 && payload.ExpiredItemCount == 0 && payload.TotalBytes == 0
	default:
		return false
	}
}

func validMemoryObserveStatus(eventType event.Type, payload event.MemoryPayload) bool {
	switch eventType {
	case event.MemoryObserveCompleted:
		return payload.Status == "completed" && payload.Error == nil
	case event.MemoryObserveFailed:
		return (payload.Status == "degraded" || payload.Status == "failed" || payload.Status == "canceled") && payload.Error != nil
	default:
		return false
	}
}

func validToolResultOffloadStatus(eventType event.Type, payload event.ToolResultOffloadPayload) bool {
	switch eventType {
	case event.ToolResultOffloadCompleted:
		if payload.Fallback != "none" || payload.Error != nil {
			return false
		}
		return (payload.Status == "inline" && payload.ObjectRefID == "") ||
			(payload.Status == "offloaded" && payload.ObjectRefID != "")
	case event.ToolResultOffloadFailed:
		return (payload.Status == "failed" || payload.Status == "canceled") && payload.Fallback == "fail_closed" && payload.Error != nil
	default:
		return false
	}
}

func validCompressionStatus(eventType event.Type, payload event.CompressionPayload) bool {
	switch eventType {
	case event.CompressionCompleted:
		return (payload.Status == "completed" || payload.Status == "insufficient") &&
			payload.StrategyName != "" && payload.StrategyVersion != "" && payload.AfterInputTokens > 0 &&
			payload.CoveredGroups > 0 && len(payload.CoveredMessageIDs) > 0 && payload.SummaryMessageID != ""
	case event.CompressionFailed:
		return (payload.Status == "failed" || payload.Status == "canceled") && payload.Error != nil
	default:
		return false
	}
}

func validContextPrepareStatus(status string) bool {
	switch status {
	case "ready", "overflow", "failed", "canceled":
		return true
	default:
		return false
	}
}

func terminalMatchesStatus(eventType event.Type, status string) bool {
	switch eventType {
	case event.RunCompleted:
		return status == "completed"
	case event.RunFailed:
		return status == "failed"
	case event.RunCanceled:
		return status == "canceled"
	default:
		return false
	}
}

func EventTypes(events []event.Event) []event.Type {
	values := make([]event.Type, len(events))
	for index, value := range events {
		values[index] = value.Type
	}
	return values
}

func DecodePayload[T any](value event.Event) (T, error) {
	var payload T
	if err := json.Unmarshal(value.Payload, &payload); err != nil {
		return payload, err
	}
	return payload, nil
}

func cloneEvent(value event.Event) event.Event {
	cloned := value
	cloned.Payload = append(json.RawMessage(nil), value.Payload...)
	return cloned
}
