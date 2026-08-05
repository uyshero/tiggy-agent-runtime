package agent

import (
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/approval"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// State 是一次 Run 的可序列化对话与用量快照；可含审批挂起信息。
//
// Continue 要求 ValidateComplete；Resume 要求 ValidateSuspended。
// Runtime 每次改动前 Clone，避免与调用方共享缓冲。
type State struct {
	Messages   []message.Message    `json:"messages"`             // 完整对话（含 System/User/Assistant/Tool）
	Usage      model.Usage          `json:"usage"`                // 累计 Token/费用
	Suspension *approval.Suspension `json:"suspension,omitempty"` // 非 nil 表示等待人工审批
}

// Clone 深拷贝 Messages 与 Suspension，Usage 为值类型直接拷贝。
func (s State) Clone() State {
	cloned := State{Messages: message.CloneAll(s.Messages), Usage: s.Usage}
	if s.Suspension != nil {
		value := s.Suspension.Clone()
		cloned.Suspension = &value
	}
	return cloned
}

// Validate 校验消息结构，并保证 ToolCall / ToolResult 成对且名称一致。
// 允许存在尚未有 Result 的 Call（挂起或执行中）；完整性看 ValidateComplete。
func (s State) Validate() error {
	if err := s.Usage.Validate(); err != nil {
		return err
	}
	messageIDs := make(map[string]struct{}, len(s.Messages))
	calls := make(map[string]string)     // callID -> tool name
	results := make(map[string]struct{}) // 已出现的 result callID
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
				// ToolCall 只能出现在 Assistant 消息里，且 callID 全局唯一。
				if value.Role != message.RoleAssistant {
					return fmt.Errorf("tool call must belong to an assistant message")
				}
				if _, exists := calls[content.ToolCall.ID]; exists {
					return fmt.Errorf("duplicate tool call id %q", content.ToolCall.ID)
				}
				calls[content.ToolCall.ID] = content.ToolCall.Name
			}
			if content.ToolResult != nil {
				// ToolResult 只能在 Tool 消息里，且必须引用已知 Call，名称一致、不重复。
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

// PendingToolCalls 返回尚未收到对应 ToolResult 的 ToolCall（按消息出现顺序）。
// Resume / closePendingTools / ValidateSuspended 都依赖这个列表。
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
				call.Arguments = append([]byte(nil), content.ToolCall.Arguments...) // 深拷贝参数
				pending = append(pending, call)
			}
		}
	}
	return pending
}

// ValidateComplete 要求：结构合法、无挂起、无 pending ToolCall。
// Continue 入口与成功终态都会调用。
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

// ValidateSuspended 校验挂起态：
// Suspension 必须按消息顺序绑定完整 pending Tool 批次；
// 参数正文仍在 Messages 里，Suspension 只存 digest 与策略处置。
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
	// 逐条核对 CallID、工具名、参数摘要，防止 Messages 被篡改后仍 Resume。
	for index, call := range pending {
		planned := s.Suspension.Calls[index]
		if planned.CallID != call.ID || planned.Tool != call.Name || planned.ArgumentsDigest != approval.ArgumentsDigest(call.Name, call.Arguments) {
			return fmt.Errorf("suspension call %d does not match pending tool call", index)
		}
	}
	return nil
}
