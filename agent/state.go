package agent

import (
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

type State struct {
	Messages []message.Message `json:"messages"`
	Usage    model.Usage       `json:"usage"`
}

func (s State) Clone() State {
	return State{Messages: message.CloneAll(s.Messages), Usage: s.Usage}
}

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
