// Package message defines model-visible messages and content blocks.
package message

import "encoding/json"

// Role identifies the author role of a model-visible message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Visibility controls whether a message is suitable for direct presentation.
// It is an execution concern, not an authorization mechanism.
type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityInternal Visibility = "internal"
)

type ContentType string

const (
	ContentText       ContentType = "text"
	ContentImage      ContentType = "image"
	ContentThinking   ContentType = "thinking"
	ContentToolCall   ContentType = "tool_call"
	ContentToolResult ContentType = "tool_result"
	ContentArtifact   ContentType = "artifact"
)

// Content is a tagged union. Exactly one payload matching Type must be set.
type Content struct {
	Type       ContentType        `json:"type"`
	Text       string             `json:"text,omitempty"`
	Image      *ImageReference    `json:"image,omitempty"`
	Thinking   *ThinkingBlock     `json:"thinking,omitempty"`
	ToolCall   *ToolCall          `json:"tool_call,omitempty"`
	ToolResult *ToolResult        `json:"tool_result,omitempty"`
	Artifact   *ArtifactReference `json:"artifact,omitempty"`
}

type ImageReference struct {
	ObjectRefID string `json:"object_ref_id,omitempty"`
	URL         string `json:"url,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// ArtifactReference identifies model-visible content stored outside context.
// ObjectRefID is opaque and must not contain a signed URL or credential.
type ArtifactReference struct {
	ObjectRefID string `json:"object_ref_id"`
	Name        string `json:"name,omitempty"`
	MediaType   string `json:"media_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Digest      string `json:"digest"`
}

type ThinkingBlock struct {
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`
}

type Message struct {
	ID         string          `json:"id"`
	Role       Role            `json:"role"`
	Visibility Visibility      `json:"visibility"`
	Content    []Content       `json:"content"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

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
