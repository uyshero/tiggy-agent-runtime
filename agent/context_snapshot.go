package agent

import (
	"encoding/json"
	"math"

	"github.com/uyshero/tiggy-agent-runtime/contextassembly"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// modelContextSnapshot describes the exact Provider Request view with fixed,
// content-free counters. projectedCount is the view before target compression.
func modelContextSnapshot(projectedCount int, messages []message.Message, tools []model.ToolDefinition) *event.ModelContextPayload {
	payload := &event.ModelContextPayload{
		ProjectedMessageCount: projectedCount,
		RequestMessageCount:   len(messages),
		ToolDefinitionCount:   len(tools),
	}
	for _, value := range messages {
		switch value.Role {
		case message.RoleSystem:
			payload.SystemMessageCount++
		case message.RoleUser:
			payload.UserMessageCount++
		case message.RoleAssistant:
			payload.AssistantMessageCount++
		case message.RoleTool:
			payload.ToolMessageCount++
		}
		switch value.Visibility {
		case message.VisibilityPublic:
			payload.PublicMessageCount++
		case message.VisibilityInternal:
			payload.InternalMessageCount++
		}
		if value.Role == message.RoleSystem && value.Visibility == message.VisibilityInternal {
			switch projectionKind(value.Metadata) {
			case contextassembly.MetadataKindInstructionStack:
				payload.InstructionStackMessages++
				addContentTextBytes(&payload.InstructionStackBytes, value.Content)
				addInstructionStackKindBytes(payload, value.Metadata)
			case "memory_recall":
				payload.RecalledMemoryMessages++
			case "context_summary":
				payload.CompressionSummaries++
			}
		}
		for _, content := range value.Content {
			addContentSnapshot(payload, content)
		}
	}
	for _, definition := range tools {
		addBytes(&payload.ToolDefinitionBytes, len(definition.Name))
		addBytes(&payload.ToolDefinitionBytes, len(definition.Description))
		addBytes(&payload.ToolDefinitionBytes, len(definition.InputSchema))
		addBytes(&payload.ToolDefinitionBytes, len(definition.OutputSchema))
		addBytes(&payload.ToolDefinitionBytes, len(definition.ConcurrencyClass))
	}
	return payload
}

func addContentSnapshot(payload *event.ModelContextPayload, content message.Content) {
	switch content.Type {
	case message.ContentText:
		payload.TextBlocks++
		addBytes(&payload.TextBytes, len(content.Text))
	case message.ContentThinking:
		payload.ThinkingBlocks++
		if content.Thinking != nil {
			addBytes(&payload.ThinkingBytes, len(content.Thinking.Text))
			addBytes(&payload.ThinkingBytes, len(content.Thinking.Signature))
		}
	case message.ContentImage:
		payload.ImageBlocks++
	case message.ContentToolCall:
		payload.ToolCallBlocks++
		if content.ToolCall != nil {
			addBytes(&payload.ToolArgumentBytes, len(content.ToolCall.Arguments))
		}
	case message.ContentToolResult:
		payload.ToolResultBlocks++
		if content.ToolResult != nil {
			for _, nested := range content.ToolResult.Content {
				addContentSnapshot(payload, nested)
			}
		}
	case message.ContentArtifact:
		payload.ArtifactBlocks++
	}
}

func addContentTextBytes(total *int64, contents []message.Content) {
	for _, content := range contents {
		switch content.Type {
		case message.ContentText:
			addBytes(total, len(content.Text))
		case message.ContentToolResult:
			if content.ToolResult != nil {
				addContentTextBytes(total, content.ToolResult.Content)
			}
		}
	}
}

func addInstructionStackKindBytes(payload *event.ModelContextPayload, metadata json.RawMessage) {
	if len(metadata) == 0 {
		return
	}
	var value contextassembly.Metadata
	if json.Unmarshal(metadata, &value) != nil {
		return
	}
	for _, entry := range value.StaticKindBytes {
		if entry.Bytes <= 0 {
			continue
		}
		switch entry.Kind {
		case contextassembly.KindSystemPrompt:
			addByteValue(&payload.InstructionStackSystemPromptBytes, entry.Bytes)
		case contextassembly.KindProject:
			addByteValue(&payload.InstructionStackProjectBytes, entry.Bytes)
		case contextassembly.KindTaskRule:
			addByteValue(&payload.InstructionStackTaskRuleBytes, entry.Bytes)
		case contextassembly.KindPolicy:
			addByteValue(&payload.InstructionStackPolicyBytes, entry.Bytes)
		case contextassembly.KindKnowledge:
			addByteValue(&payload.InstructionStackKnowledgeBytes, entry.Bytes)
		case contextassembly.KindSkill:
			addByteValue(&payload.InstructionStackSkillBytes, entry.Bytes)
		}
	}
}

func projectionKind(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}
	var value struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(metadata, &value) != nil {
		return ""
	}
	return value.Kind
}

func addBytes(total *int64, size int) {
	if size <= 0 || *total == math.MaxInt64 {
		return
	}
	addByteValue(total, int64(size))
}

func addByteValue(total *int64, value int64) {
	if value <= 0 || *total == math.MaxInt64 {
		return
	}
	if value > math.MaxInt64-*total {
		*total = math.MaxInt64
		return
	}
	*total += value
}
