// Package message 定义模型可见的消息与内容块。
//
// 这是 Runtime 与模型之间的共享语言：Agent State、Model Request/Response、
// Tool Result 都建立在这些类型上。协议包只依赖标准库。
package message

import "encoding/json"

// Role 标识模型可见消息的作者角色。
type Role string

const (
	RoleSystem    Role = "system"    // 系统提示 / 内部投影（如回忆）
	RoleUser      Role = "user"      // 用户输入
	RoleAssistant Role = "assistant" // 模型回复（可含 ToolCall）
	RoleTool      Role = "tool"      // 工具结果（含 ToolResult）
)

// Visibility 控制消息是否适合直接展示；这是执行语义，不是授权机制。
type Visibility string

const (
	VisibilityPublic   Visibility = "public"   // 可对终端用户展示
	VisibilityInternal Visibility = "internal" // 仅 Runtime/模型上下文使用
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
// 例如 Type=text 时只用 Text；Type=tool_call 时只用 ToolCall。
type Content struct {
	Type       ContentType        `json:"type"`
	Text       string             `json:"text,omitempty"`
	Image      *ImageReference    `json:"image,omitempty"`
	Thinking   *ThinkingBlock     `json:"thinking,omitempty"`
	ToolCall   *ToolCall          `json:"tool_call,omitempty"`
	ToolResult *ToolResult        `json:"tool_result,omitempty"`
	Artifact   *ArtifactReference `json:"artifact,omitempty"`
}

// ImageReference 引用图片对象或 URL（二选一或按供应商约定）。
type ImageReference struct {
	ObjectRefID string `json:"object_ref_id,omitempty"`
	URL         string `json:"url,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
	Detail      string `json:"detail,omitempty"` // 如 low/high，由供应商解释
}

// ArtifactReference 标识存放在上下文之外的模型可见内容。
// ObjectRefID 不透明，不得包含签名 URL 或凭据。
type ArtifactReference struct {
	ObjectRefID string `json:"object_ref_id"`
	Name        string `json:"name,omitempty"`
	MediaType   string `json:"media_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Digest      string `json:"digest"` // 内容完整性摘要
}

// ThinkingBlock 表示可选的推理过程文本或已脱敏占位。
type ThinkingBlock struct {
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"` // 供应商签名/校验字段
	Redacted  bool   `json:"redacted,omitempty"`  // true 时 Text 可能为空
}

// Message 是一轮对话中的单条消息。
type Message struct {
	ID         string          `json:"id"`
	Role       Role            `json:"role"`
	Visibility Visibility      `json:"visibility"`
	Content    []Content       `json:"content"`            // 至少一块
	Metadata   json.RawMessage `json:"metadata,omitempty"` // 扩展元数据（如 memory_recall）
}

// ToolCall 是助手发起的一次工具调用。
type ToolCall struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`                      // 限定名，如 builtin.read
	Arguments      json.RawMessage `json:"arguments"`                 // 原始 JSON 参数
	ArgumentsError string          `json:"arguments_error,omitempty"` // 解析失败时的说明
}

// ToolResult 是对某次 ToolCall 的结果；CallID/Name 必须与对应 Call 一致。
type ToolResult struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Content   []Content       `json:"content"`
	State     json.RawMessage `json:"state,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`  // 模型可见失败
	Retryable bool            `json:"retryable,omitempty"` // 是否值得重试
}
