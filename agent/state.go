package agent

import (
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/approval"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// State 是一次 Run 的可序列化对话与用量快照；可含审批挂起信息。
type State struct {
	Messages   []message.Message    `json:"messages"`
	Usage      model.Usage          `json:"usage"`
	Suspension *approval.Suspension `json:"suspension,omitempty"`
}

// Clone 深拷贝 Messages 与 Suspension，避免与调用方共享缓冲。
func (s State) Clone() State {
	cloned := State{Messages: message.CloneAll(s.Messages), Usage: s.Usage}
	if s.Suspension != nil {
		value := s.Suspension.Clone()
		cloned.Suspension = &value
	}
	return cloned
}

// Validate 校验消息结构，并保证 ToolCall / ToolResult 成对且名称一致。
func (s State) Validate() error {
	if err := s.Usage.Validate(); err != nil {
		return err
	}
	messageIDs := make(map[string]struct{}, len(s.Messages))
	calls := make(map[string]string)
	results := make(map[string]struct{})
	for index, value := range s.Messages {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
		if _, exists := messageIDs[value.ID]; exists {
			return fmt.Errorf("duplicate message id %q", value.ID)
		}
		messageIDs[value.ID] = struct{}{}
		for _, content := range value.Content {
			if content.ToolCall != nil {
				if value.Role != message.RoleAssistant {
					return fmt.Errorf("tool call must belong to an assistant message")
				}
				if _, exists := calls[content.ToolCall.ID]; exists {
					return fmt.Errorf("duplicate tool call id %q", content.ToolCall.ID)
				}
				calls[content.ToolCall.ID] = content.ToolCall.Name
			}
			if content.ToolResult != nil {
				if value.Role != message.RoleTool {
					return fmt.Errorf("tool result must belong to a tool message")
				}
				callName, exists := calls[content.ToolResult.CallID]
				if !exists {
					return fmt.Errorf("tool result references unknown call %q", content.ToolResult.CallID)
				}
				if callName != content.ToolResult.Name {
					return fmt.Errorf("tool result name %q does not match call %q", content.ToolResult.Name, callName)
				}
				if _, exists := results[content.ToolResult.CallID]; exists {
					return fmt.Errorf("duplicate tool result for call %q", content.ToolResult.CallID)
				}
				results[content.ToolResult.CallID] = struct{}{}
			}
		}
	}
	return nil
}

// PendingToolCalls 返回尚未收到对应 ToolResult 的 ToolCall。
func (s State) PendingToolCalls() []message.ToolCall {
	results := make(map[string]struct{})
	for _, value := range s.Messages {
		for _, content := range value.Content {
			if content.ToolResult != nil {
				results[content.ToolResult.CallID] = struct{}{}
			}
		}
	}
	var pending []message.ToolCall
	for _, value := range s.Messages {
		for _, content := range value.Content {
			if content.ToolCall == nil {
				continue
			}
			if _, exists := results[content.ToolCall.ID]; !exists {
				call := *content.ToolCall
				call.Arguments = append([]byte(nil), content.ToolCall.Arguments...)
				pending = append(pending, call)
			}
		}
	}
	return pending
}

func (s State) ValidateComplete() error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Suspension != nil {
		return fmt.Errorf("complete state contains suspension %q", s.Suspension.ID)
	}
	pending := s.PendingToolCalls()
	if len(pending) > 0 {
		names := make([]string, len(pending))
		for index, call := range pending {
			names[index] = call.ID
		}
		return fmt.Errorf("state has pending tool calls: %s", strings.Join(names, ", "))
	}
	return nil
}

// ValidateSuspended verifies that Suspension binds the complete pending Tool
// batch in message order. Tool arguments remain in Messages; Suspension stores
// only their digests and policy dispositions.
func (s State) ValidateSuspended() error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Suspension == nil {
		return fmt.Errorf("suspended state has no suspension")
	}
	if err := s.Suspension.Validate(); err != nil {
		return err
	}
	if s.Suspension.TurnStart >= len(s.Messages) {
		return fmt.Errorf("suspension turn start is outside message state")
	}
	pending := s.PendingToolCalls()
	if len(pending) != len(s.Suspension.Calls) {
		return fmt.Errorf("pending tool count %d does not match suspension call count %d", len(pending), len(s.Suspension.Calls))
	}
	for index, call := range pending {
		planned := s.Suspension.Calls[index]
		if planned.CallID != call.ID || planned.Tool != call.Name || planned.ArgumentsDigest != approval.ArgumentsDigest(call.Name, call.Arguments) {
			return fmt.Errorf("suspension call %d does not match pending tool call", index)
		}
	}
	return nil
}
