package message

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const maxContentDepth = 4 // Content/ToolResult 嵌套深度上限，防止恶意深层嵌套

// Validate 校验消息角色、可见性与全部 Content 块。
func (m Message) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("message id is required")
	}
	switch m.Role {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
	default:
		return fmt.Errorf("unsupported message role %q", m.Role)
	}
	if m.Visibility != VisibilityPublic && m.Visibility != VisibilityInternal {
		return fmt.Errorf("unsupported message visibility %q", m.Visibility)
	}
	if len(m.Content) == 0 {
		return fmt.Errorf("message content is required")
	}
	for index, content := range m.Content {
		if err := validateContent(content, 0); err != nil {
			return fmt.Errorf("content %d: %w", index, err)
		}
	}
	if len(m.Metadata) > 0 && !json.Valid(m.Metadata) {
		return fmt.Errorf("message metadata must be valid JSON")
	}
	return nil
}

// Validate 校验 ToolCall：ID/Name 非空，Arguments 必须是合法 JSON Object；
// ArgumentsError 可额外记录 Provider 解析失败，但不能替代安全的占位 Arguments。
func (c ToolCall) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("tool call id is required")
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("tool call name is required")
	}
	if len(c.Arguments) == 0 || !json.Valid(c.Arguments) {
		return fmt.Errorf("tool call arguments must be valid JSON")
	}
	trimmed := bytes.TrimSpace(c.Arguments)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("tool call arguments must be a JSON object")
	}
	return nil
}

func validateContent(content Content, depth int) error {
	if depth > maxContentDepth {
		return fmt.Errorf("content nesting exceeds limit")
	}
	if payloadCount(content) != 1 {
		return fmt.Errorf("content must contain exactly one payload")
	}
	switch content.Type {
	case ContentText:
		if content.Text == "" {
			return fmt.Errorf("text content is empty")
		}
	case ContentImage:
		if content.Image == nil || (strings.TrimSpace(content.Image.ObjectRefID) == "" && strings.TrimSpace(content.Image.URL) == "") {
			return fmt.Errorf("image reference is required")
		}
	case ContentThinking:
		if content.Thinking == nil {
			return fmt.Errorf("thinking block is required")
		}
	case ContentToolCall:
		if content.ToolCall == nil {
			return fmt.Errorf("tool call is required")
		}
		if err := content.ToolCall.Validate(); err != nil {
			return err
		}
	case ContentToolResult:
		if content.ToolResult == nil {
			return fmt.Errorf("tool result is required")
		}
		if err := content.ToolResult.validate(depth + 1); err != nil {
			return err
		}
	case ContentArtifact:
		if content.Artifact == nil {
			return fmt.Errorf("artifact reference is required")
		}
		if err := content.Artifact.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported content type %q", content.Type)
	}
	return nil
}

func (r ArtifactReference) Validate() error {
	if strings.TrimSpace(r.ObjectRefID) == "" || r.ObjectRefID != strings.TrimSpace(r.ObjectRefID) || len(r.ObjectRefID) > 1024 {
		return fmt.Errorf("artifact object reference id is invalid")
	}
	if strings.Contains(r.ObjectRefID, "://") || strings.ContainsAny(r.ObjectRefID, "?#") {
		return fmt.Errorf("artifact object reference id must be opaque")
	}
	if strings.TrimSpace(r.MediaType) == "" || r.MediaType != strings.TrimSpace(r.MediaType) || len(r.MediaType) > 255 {
		return fmt.Errorf("artifact media type is invalid")
	}
	if r.SizeBytes <= 0 {
		return fmt.Errorf("artifact size must be positive")
	}
	if r.Name != strings.TrimSpace(r.Name) || len(r.Name) > 512 {
		return fmt.Errorf("artifact name is invalid")
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(r.Digest, prefix) || len(r.Digest) != len(prefix)+64 {
		return fmt.Errorf("artifact digest must be sha256")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(r.Digest, prefix)); err != nil {
		return fmt.Errorf("artifact digest must be sha256: %w", err)
	}
	return nil
}

func (r ToolResult) Validate() error {
	return r.validate(0)
}

func (r ToolResult) validate(depth int) error {
	if strings.TrimSpace(r.CallID) == "" {
		return fmt.Errorf("tool result call id is required")
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("tool result name is required")
	}
	if len(r.Content) == 0 {
		return fmt.Errorf("tool result content is required")
	}
	for index, content := range r.Content {
		if err := validateContent(content, depth); err != nil {
			return fmt.Errorf("tool result content %d: %w", index, err)
		}
	}
	if len(r.State) > 0 && !json.Valid(r.State) {
		return fmt.Errorf("tool result state must be valid JSON")
	}
	return nil
}

func payloadCount(content Content) int {
	count := 0
	if content.Text != "" {
		count++
	}
	if content.Image != nil {
		count++
	}
	if content.Thinking != nil {
		count++
	}
	if content.ToolCall != nil {
		count++
	}
	if content.ToolResult != nil {
		count++
	}
	if content.Artifact != nil {
		count++
	}
	return count
}
