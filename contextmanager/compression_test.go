package contextmanager_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestCompressionPreservesSystemActiveTurnAndToolGroups(t *testing.T) {
	t.Parallel()

	messages := compressionHistory()
	request, err := contextmanager.NewCompressionRequest(validRequest().Model, messages, overflowPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Groups) != 1 || len(request.Groups[0].Messages) != 3 {
		t.Fatalf("compression groups = %#v", request.Groups)
	}
	compressed, err := contextmanager.ApplyCompression(messages, request, contextmanager.CompressionDecision{
		Strategy:      contextmanager.CompressionStrategy{Name: "test-summary", Version: "1"},
		CoveredGroups: 1, Summary: "The user looked up the old value and received 42.",
	}, "summary_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed.Messages) != 3 || compressed.Messages[0].ID != "system_1" || compressed.Messages[1].ID != "summary_1" || compressed.Messages[2].ID != "user_new" {
		t.Fatalf("compressed messages = %#v", compressed.Messages)
	}
	if got := compressed.CoveredMessageIDs; len(got) != 3 || got[0] != "user_old" || got[2] != "tool_old" {
		t.Fatalf("covered message ids = %#v", got)
	}
	var provenance struct {
		Kind             string   `json:"kind"`
		SourceMessageIDs []string `json:"source_message_ids"`
	}
	if err := json.Unmarshal(compressed.Messages[1].Metadata, &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance.Kind != "context_summary" || len(provenance.SourceMessageIDs) != 3 {
		t.Fatalf("summary provenance = %#v", provenance)
	}
}

func TestCompressionRejectsInvalidCoverageAndChangedSource(t *testing.T) {
	t.Parallel()

	messages := compressionHistory()
	request, err := contextmanager.NewCompressionRequest(validRequest().Model, messages, overflowPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	decision := contextmanager.CompressionDecision{
		Strategy:      contextmanager.CompressionStrategy{Name: "test-summary", Version: "1"},
		CoveredGroups: 2, Summary: "summary",
	}
	_, err = contextmanager.ApplyCompression(messages, request, decision, "summary_1")
	assertContextCode(t, err, contextmanager.ErrorInvalidCompression)

	decision.CoveredGroups = 1
	changed := append([]message.Message(nil), messages...)
	changed[1] = message.Clone(changed[1])
	changed[1].Content[0].Text = "changed"
	_, err = contextmanager.ApplyCompression(changed, request, decision, "summary_1")
	assertContextCode(t, err, contextmanager.ErrorInvalidCompression)
}

func TestCompressionProtectsUnresolvedToolCall(t *testing.T) {
	t.Parallel()

	messages := compressionHistory()
	messages = messages[:3]
	_, err := contextmanager.NewCompressionRequest(validRequest().Model, messages, overflowPlan(t))
	var contextErr *contextmanager.Error
	if !errors.As(err, &contextErr) || contextErr.Code != contextmanager.ErrorInvalidRequest {
		t.Fatalf("error = %v, want invalid request", err)
	}
}

func TestProviderContextLengthCompressionRequiresFittingPlan(t *testing.T) {
	t.Parallel()

	messages := compressionHistory()
	fitting := fittingPlan(t)
	request, err := contextmanager.NewProviderContextLengthCompressionRequest(validRequest().Model, messages, fitting)
	if err != nil {
		t.Fatal(err)
	}
	if request.Trigger != contextmanager.CompressionTriggerProviderContextLength || request.Plan.OverflowTokens != 0 || !request.Plan.Fits {
		t.Fatalf("recovery request = %#v", request)
	}
	if _, err := contextmanager.NewProviderContextLengthCompressionRequest(validRequest().Model, messages, overflowPlan(t)); err == nil {
		t.Fatal("Provider recovery accepted an overflow Plan")
	}
	request.Trigger = "unsupported"
	if err := request.Validate(); err == nil {
		t.Fatal("CompressionRequest accepted an unsupported Trigger")
	}
}

func compressionHistory() []message.Message {
	return []message.Message{
		{ID: "system_1", Role: message.RoleSystem, Visibility: message.VisibilityInternal, Content: []message.Content{{Type: message.ContentText, Text: "Be reliable."}}},
		{ID: "user_old", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "Look up the old value."}}},
		{ID: "assistant_old", Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call_old", Name: "lookup", Arguments: json.RawMessage(`{}`)}}}},
		{ID: "tool_old", Role: message.RoleTool, Visibility: message.VisibilityInternal, Content: []message.Content{{Type: message.ContentToolResult, ToolResult: &message.ToolResult{CallID: "call_old", Name: "lookup", Content: []message.Content{{Type: message.ContentText, Text: "42"}}}}}},
		{ID: "user_new", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "Use it now."}}},
	}
}

func overflowPlan(t *testing.T) contextmanager.Plan {
	t.Helper()
	planner := newPlanner(t, agenttest.FuncTokenCounter(func(_ context.Context, _ model.TokenRequest) (int64, error) { return 900, nil }))
	plan, err := planner.Plan(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func fittingPlan(t *testing.T) contextmanager.Plan {
	t.Helper()
	planner := newPlanner(t, agenttest.FuncTokenCounter(func(_ context.Context, _ model.TokenRequest) (int64, error) { return 400, nil }))
	plan, err := planner.Plan(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
