package model_test

import (
	"encoding/json"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestRequestValidationDoesNotRequireEnterpriseIdentity(t *testing.T) {
	t.Parallel()

	request := model.Request{
		RunID:   "run_1",
		CallID:  "model_1",
		Purpose: model.PurposeAgent,
		Model:   model.Identity{Provider: "test", Model: "fake"},
		Messages: []message.Message{{
			ID:         "msg_1",
			Role:       message.RoleUser,
			Visibility: message.VisibilityPublic,
			Content:    []message.Content{{Type: message.ContentText, Text: "hello"}},
		}},
		Tools:           []model.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		MaxOutputTokens: 256,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestUsageValidationAndAddition(t *testing.T) {
	t.Parallel()

	if err := (model.Usage{InputTokens: -1}).Validate(); err == nil {
		t.Fatal("negative usage accepted")
	}
	combined := (model.Usage{InputTokens: 3, TotalTokens: 3, Source: model.UsageSourceProvider}).Add(
		model.Usage{OutputTokens: 2, TotalTokens: 2, Source: model.UsageSourceEstimated},
	)
	if combined.TotalTokens != 5 || combined.Source != model.UsageSourceEstimated {
		t.Fatalf("Add() = %+v", combined)
	}
}

func TestToolDefinitionRejectsUnknownConcurrencyClass(t *testing.T) {
	t.Parallel()

	definition := model.ToolDefinition{
		Name: "unsafe", InputSchema: json.RawMessage(`{"type":"object"}`), ConcurrencyClass: "sometimes",
	}
	if err := definition.Validate(); err == nil {
		t.Fatal("unknown concurrency class accepted")
	}
}
