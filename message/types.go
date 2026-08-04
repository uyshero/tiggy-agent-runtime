// Package message 定义模型可见的消息与内容块。
package message

import "encoding/json"

// Role 标识模型可见消息的作者角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Visibility 控制消息是否适合直接展示；这是执行语义，不是授权机制。
type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityInternal Visibility = "internal"
)

// ContentType 标识 Content 联合体中的有效载荷种类。
type ContentType string

const (
	ContentText       ContentType = "text"
	ContentImage      ContentType = "image"
	ContentThinking   ContentType = "thinking"
	ContentToolCall   ContentType = "tool_call"
	ContentToolResult ContentType = "tool_result"
	ContentArtifact   ContentType = "artifact"
)

// Content 是带标签的联合体；必须且仅有一个与 Type 匹配的载荷。
type Content struct {
	Type       ContentType        `json:"type"`
	Text       string             `json:"text,omitempty"`
	Image      *ImageReference    `json:"image,omitempty"`
	Thinking   *ThinkingBlock     `json:"thinking,omitempty"`
	ToolCall   *ToolCall          `json:"tool_call,omitempty"`
	ToolResult *ToolResult        `json:"tool_result,omitempty"`
	Artifact   *ArtifactReference `json:"artifact,omitempty"`
}

// ImageReference 引用图片对象或 URL。
type ImageReference struct {
	ObjectRefID string `json:"object_ref_id,omitempty"`
	URL         string `json:"url,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// ArtifactReference 标识存放在上下文之外的模型可见内容。
// ObjectRefID 不透明，不得包含签名 URL 或凭据。
type ArtifactReference struct {
	ObjectRefID string `json:"object_ref_id"`
	Name        string `json:"name,omitempty"`
	MediaType   string `json:"media_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Digest      string `json:"digest"`
}

// ThinkingBlock 表示可选的推理过程文本或已脱敏占位。
type ThinkingBlock struct {
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`
}

// Message 是一轮对话中的单条消息。
type Message struct {
	ID         string          `json:"id"`
	Role       Role            `json:"role"`
	Visibility Visibility      `json:"visibility"`
	Content    []Content       `json:"content"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

// ToolCall 是助手发起的一次工具调用。
type ToolCall struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Arguments      json.RawMessage `json:"arguments"`
	ArgumentsError string          `json:"arguments_error,omitempty"`
}

type ToolResult struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Content   []Content       `json:"content"`
	State     json.RawMessage `json:"state,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Retryable bool            `json:"retryable,omitempty"`
}
