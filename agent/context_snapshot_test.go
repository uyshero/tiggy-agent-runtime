package agent

import (
	"encoding/json"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestModelContextSnapshotCountsProjectionsAndContentWithoutBodies(t *testing.T) {
	t.Parallel()

	messages := []message.Message{
		{ID: "system", Role: message.RoleSystem, Visibility: message.VisibilityInternal, Content: []message.Content{{Type: message.ContentText, Text: "a"}}},
		{ID: "memory", Role: message.RoleSystem, Visibility: message.VisibilityInternal, Content: []message.Content{{Type: message.ContentText, Text: "bb"}}, Metadata: json.RawMessage(`{"kind":"memory_recall"}`)},
		{ID: "summary", Role: message.RoleSystem, Visibility: message.VisibilityInternal, Content: []message.Content{{Type: message.ContentText, Text: "ccc"}}, Metadata: json.RawMessage(`{"kind":"context_summary"}`)},
		{ID: "assistant", Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{
			{Type: message.ContentText, Text: "dddd"},
			{Type: message.ContentThinking, Thinking: &message.ThinkingBlock{Text: "f", Signature: "gg"}},
			{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call", Name: "x", Arguments: json.RawMessage(`{}`)}},
		}},
		{ID: "tool", Role: message.RoleTool, Visibility: message.VisibilityInternal, Content: []message.Content{{
			Type: message.ContentToolResult, ToolResult: &message.ToolResult{CallID: "call", Name: "x", Content: []message.Content{
				{Type: message.ContentText, Text: "eeeee"},
				{Type: message.ContentArtifact, Artifact: &message.ArtifactReference{ObjectRefID: "object", MediaType: "text/plain", SizeBytes: 5, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
			}},
		}}},
		{ID: "user", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentImage, Image: &message.ImageReference{URL: "https://example.test/image.png"}}}},
	}
	tools := []model.ToolDefinition{{
		Name: "x", Description: "yy", InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`),
		ConcurrencyClass: model.ToolConcurrencySafe,
	}}

	payload := modelContextSnapshot(len(messages), messages, tools)
	if err := payload.Validate(); err != nil {
		t.Fatal(err)
	}
	if payload.RequestMessageCount != 6 || payload.SystemMessageCount != 3 || payload.UserMessageCount != 1 || payload.AssistantMessageCount != 1 || payload.ToolMessageCount != 1 {
		t.Fatalf("role counts = %#v", payload)
	}
	if payload.InternalMessageCount != 4 || payload.PublicMessageCount != 2 || payload.RecalledMemoryMessages != 1 || payload.CompressionSummaries != 1 {
		t.Fatalf("projection counts = %#v", payload)
	}
	if payload.TextBlocks != 5 || payload.TextBytes != 15 || payload.ThinkingBlocks != 1 || payload.ThinkingBytes != 3 || payload.ImageBlocks != 1 {
		t.Fatalf("content counts = %#v", payload)
	}
	if payload.ToolCallBlocks != 1 || payload.ToolArgumentBytes != 2 || payload.ToolResultBlocks != 1 || payload.ArtifactBlocks != 1 {
		t.Fatalf("Tool counts = %#v", payload)
	}
	if payload.ToolDefinitionCount != 1 || payload.ToolDefinitionBytes != 11 {
		t.Fatalf("Tool Definition counts = %#v", payload)
	}
}
