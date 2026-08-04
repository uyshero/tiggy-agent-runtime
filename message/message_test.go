package message_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/message"
)

func TestValidateRejectsAmbiguousContent(t *testing.T) {
	t.Parallel()

	value := message.Message{
		ID:         "msg_1",
		Role:       message.RoleAssistant,
		Visibility: message.VisibilityPublic,
		Content: []message.Content{{
			Type:  message.ContentText,
			Text:  "hello",
			Image: &message.ImageReference{URL: "https://example.test/image.png"},
		}},
	}
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want ambiguous payload error")
	}
}

func TestToolCallRequiresJSONObject(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`null`, `[]`, `"value"`, `42`, `{broken`} {
		call := message.ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(raw)}
		if err := call.Validate(); err == nil {
			t.Errorf("Validate() with %q error = nil, want error", raw)
		}
	}
}

func TestCloneDoesNotAliasMutableData(t *testing.T) {
	t.Parallel()

	original := message.Message{
		ID:         "msg_1",
		Role:       message.RoleTool,
		Visibility: message.VisibilityInternal,
		Metadata:   json.RawMessage(`{"source":"original"}`),
		Content: []message.Content{{
			Type: message.ContentToolResult,
			ToolResult: &message.ToolResult{
				CallID:  "call_1",
				Name:    "lookup",
				State:   json.RawMessage(`{"cursor":1}`),
				Content: []message.Content{{Type: message.ContentText, Text: "done"}},
			},
		}},
	}
	cloned := message.Clone(original)
	cloned.Metadata[2] = 'X'
	cloned.Content[0].ToolResult.State[2] = 'X'

	if string(original.Metadata) != `{"source":"original"}` || string(original.Content[0].ToolResult.State) != `{"cursor":1}` {
		t.Fatal("mutating clone changed original message")
	}
}

func TestArtifactReferenceValidationAndClone(t *testing.T) {
	t.Parallel()

	value := message.Message{
		ID: "msg_artifact", Role: message.RoleTool, Visibility: message.VisibilityInternal,
		Content: []message.Content{{Type: message.ContentToolResult, ToolResult: &message.ToolResult{
			CallID: "call_1", Name: "report", Content: []message.Content{{
				Type: message.ContentArtifact, Artifact: &message.ArtifactReference{
					ObjectRefID: "objects/result-1", Name: "result.json", MediaType: "application/json",
					SizeBytes: 128, Digest: "sha256:" + strings.Repeat("a", 64),
				},
			}},
		}}},
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	cloned := message.Clone(value)
	cloned.Content[0].ToolResult.Content[0].Artifact.ObjectRefID = "changed"
	if value.Content[0].ToolResult.Content[0].Artifact.ObjectRefID != "objects/result-1" {
		t.Fatal("artifact reference clone aliases the source")
	}
	for _, objectID := range []string{"https://example.test/signed", "object?id=secret", " object "} {
		invalid := message.Clone(value)
		invalid.Content[0].ToolResult.Content[0].Artifact.ObjectRefID = objectID
		if err := invalid.Validate(); err == nil {
			t.Fatalf("Validate accepted object reference %q", objectID)
		}
	}
}
