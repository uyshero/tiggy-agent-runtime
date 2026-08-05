package compression_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	compressioneval "github.com/uyshero/tiggy-agent-runtime/evaluation/compression"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestEvaluateCompressorQuality(t *testing.T) {
	t.Parallel()

	config := qualityConfig(t, compressorFunc(func(_ context.Context, request contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		request.Groups[0].Messages[0].Content[0].Text = "mutated owned copy"
		return contextmanager.CompressionDecision{
			Strategy:      contextmanager.CompressionStrategy{Name: "fixture-summary", Version: "1"},
			CoveredGroups: 1,
			Summary:       "Account owner is Ada. The lookup tool returned 42.",
		}, nil
	}))
	report, err := compressioneval.Evaluate(context.Background(), config, []compressioneval.Case{qualityCase()})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() || report.SchemaVersion != compressioneval.SchemaV1 || report.Summary.Total != 1 {
		t.Fatalf("report = %#v", report)
	}
	result := report.Cases[0]
	if result.BeforeInputTokens != 900 || result.AfterInputTokens != 400 || result.CoveredGroups != 1 || result.CoveredMessages != 3 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Facts) != 2 || !result.Facts[0].Passed || !result.Facts[1].Passed {
		t.Fatalf("facts = %#v", result.Facts)
	}
	if !result.Checks.ProtectedMessagesPreserved || !result.Checks.ProvenanceExact || !result.Checks.CompressedBudgetFits {
		t.Fatalf("checks = %#v", result.Checks)
	}
}

func TestEvaluateReportsMissingFact(t *testing.T) {
	t.Parallel()

	definition := qualityCase()
	definition.RequiredFacts[1].AnyOf = []string{"lookup returned 99"}
	report, err := compressioneval.Evaluate(context.Background(), qualityConfig(t, validCompressor()), []compressioneval.Case{definition})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed() || report.Cases[0].Checks.RequiredFactsPreserved || report.Cases[0].Facts[1].Passed {
		t.Fatalf("missing fact passed: %#v", report.Cases[0])
	}
	if !strings.Contains(report.Cases[0].Failure, "required facts") {
		t.Fatalf("failure = %q", report.Cases[0].Failure)
	}
}

func TestEvaluateContainsCompressorPanic(t *testing.T) {
	t.Parallel()

	report, err := compressioneval.Evaluate(context.Background(), qualityConfig(t, compressorFunc(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		panic("summary backend failed")
	})), []compressioneval.Case{qualityCase()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed() || report.Cases[0].FailureCode != "compressor_failed" || report.Cases[0].Failure != "Compressor panicked." {
		t.Fatalf("panic result = %#v", report.Cases[0])
	}
}

func TestEvaluateDoesNotReportDelegateErrorDetails(t *testing.T) {
	t.Parallel()

	const secret = "prompt-body-should-not-be-recorded"
	report, err := compressioneval.Evaluate(context.Background(), qualityConfig(t, compressorFunc(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		return contextmanager.CompressionDecision{}, errors.New(secret)
	})), []compressioneval.Case{qualityCase()})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := compressioneval.MarshalJSONReport(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || report.Cases[0].Failure != "Compressor returned an error." {
		t.Fatalf("unsafe report = %s", encoded)
	}
}

func TestEvaluateEnforcesSummaryAndBudgetBounds(t *testing.T) {
	t.Parallel()

	definition := qualityCase()
	definition.MaxSummaryBytes = 10
	report, err := compressioneval.Evaluate(context.Background(), qualityConfig(t, validCompressor()), []compressioneval.Case{definition})
	if err != nil {
		t.Fatal(err)
	}
	if report.Cases[0].Checks.SummaryBounded || report.Cases[0].Passed {
		t.Fatalf("oversized summary passed: %#v", report.Cases[0])
	}

	config := qualityConfig(t, validCompressor())
	config.Planner = plannerFunc(func(_ context.Context, request contextmanager.Request) (contextmanager.Plan, error) {
		return planFor(request, 900), nil
	})
	report, err = compressioneval.Evaluate(context.Background(), config, []compressioneval.Case{qualityCase()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Cases[0].Checks.CompressedBudgetFits || report.Cases[0].Passed {
		t.Fatalf("overflowing compressed Request passed: %#v", report.Cases[0])
	}
}

func TestEvaluateRejectsInvalidSuiteBeforeDelegates(t *testing.T) {
	t.Parallel()

	calls := 0
	config := qualityConfig(t, compressorFunc(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		calls++
		return contextmanager.CompressionDecision{}, nil
	}))
	definition := qualityCase()
	definition.RequiredFacts[0].SourceMessageIDs = []string{"missing"}
	if _, err := compressioneval.Evaluate(context.Background(), config, []compressioneval.Case{definition}); err == nil {
		t.Fatal("Evaluate accepted an unknown fact source")
	}
	if calls != 0 {
		t.Fatalf("Compressor calls = %d, want 0", calls)
	}

	definition = qualityCase()
	definition.Request.Messages[4].ID = "system"
	if _, err := compressioneval.Evaluate(context.Background(), config, []compressioneval.Case{definition}); err == nil {
		t.Fatal("Evaluate accepted duplicate Message IDs")
	}
	if calls != 0 {
		t.Fatalf("Compressor calls after invalid conversation = %d, want 0", calls)
	}
}

func TestCompressionQualityReportRendering(t *testing.T) {
	t.Parallel()

	report, err := compressioneval.Evaluate(context.Background(), qualityConfig(t, validCompressor()), []compressioneval.Case{qualityCase()})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := compressioneval.MarshalJSONReport(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"schema_version": "`+compressioneval.SchemaV1+`"`) {
		t.Fatalf("JSON = %s", encoded)
	}
	markdown := compressioneval.Markdown(report)
	for _, required := range []string{"# Tiggy Compressor Quality Report", "`tool-fact-retention`", "fixture-summary@1", "PASS"} {
		if !strings.Contains(markdown, required) {
			t.Fatalf("Markdown missing %q:\n%s", required, markdown)
		}
	}
}

func qualityConfig(t *testing.T, compressor contextmanager.Compressor) compressioneval.Config {
	t.Helper()
	planner, err := contextmanager.NewTokenPlanner(contextmanager.TokenPlannerOptions{
		Counter: tokenCounterFunc(func(_ context.Context, request model.TokenRequest) (int64, error) {
			if len(request.Messages) > 3 {
				return 900, nil
			}
			return 400, nil
		}),
		CountSource: contextmanager.CountSourceEstimated, ContextWindowTokens: 1_000, SafetyMarginTokens: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	return compressioneval.Config{Planner: planner, Compressor: compressor}
}

func qualityCase() compressioneval.Case {
	return compressioneval.Case{
		ID: "tool-fact-retention",
		Request: contextmanager.Request{
			Model: model.Identity{Provider: "test", Model: "compressor"},
			Messages: []message.Message{
				{ID: "system", Role: message.RoleSystem, Visibility: message.VisibilityInternal, Content: []message.Content{{Type: message.ContentText, Text: "Be precise."}}},
				{ID: "user_old", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "The account owner is Ada. Look up the value."}}},
				{ID: "assistant_old", Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"key":"value"}`)}}}},
				{ID: "tool_old", Role: message.RoleTool, Visibility: message.VisibilityInternal, Content: []message.Content{{Type: message.ContentToolResult, ToolResult: &message.ToolResult{CallID: "call_1", Name: "lookup", Content: []message.Content{{Type: message.ContentText, Text: "42"}}}}}},
				{ID: "user_active", Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "Use the owner and value now."}}},
			},
			OutputReserveTokens: 100,
		},
		RequiredFacts: []compressioneval.RequiredFact{
			{ID: "account-owner", SourceMessageIDs: []string{"user_old"}, AnyOf: []string{"owner is Ada", "owner: Ada"}},
			{ID: "lookup-value", SourceMessageIDs: []string{"assistant_old", "tool_old"}, AnyOf: []string{"lookup tool returned 42", "lookup returned 42"}},
		},
		MaxSummaryBytes: 256,
	}
}

func validCompressor() contextmanager.Compressor {
	return compressorFunc(func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
		return contextmanager.CompressionDecision{
			Strategy:      contextmanager.CompressionStrategy{Name: "fixture-summary", Version: "1"},
			CoveredGroups: 1,
			Summary:       "Account owner is Ada. The lookup tool returned 42.",
		}, nil
	})
}

type compressorFunc func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error)

func (f compressorFunc) Compress(ctx context.Context, request contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
	return f(ctx, request)
}

type tokenCounterFunc func(context.Context, model.TokenRequest) (int64, error)

func (f tokenCounterFunc) CountTokens(ctx context.Context, request model.TokenRequest) (int64, error) {
	return f(ctx, request)
}

type plannerFunc func(context.Context, contextmanager.Request) (contextmanager.Plan, error)

func (f plannerFunc) Plan(ctx context.Context, request contextmanager.Request) (contextmanager.Plan, error) {
	return f(ctx, request)
}

func planFor(request contextmanager.Request, inputTokens int64) contextmanager.Plan {
	const window = int64(1_000)
	const margin = int64(50)
	available := window - request.OutputReserveTokens - margin
	overflow := inputTokens - available
	if overflow < 0 {
		overflow = 0
	}
	remaining := available - inputTokens
	if remaining < 0 {
		remaining = 0
	}
	pressure := contextmanager.PressureOverflow
	if overflow == 0 {
		pressure = contextmanager.PressureNormal
	}
	return contextmanager.Plan{
		Model: request.Model, CountSource: contextmanager.CountSourceEstimated,
		ContextWindowTokens: window, InputTokens: inputTokens,
		OutputReserveTokens: request.OutputReserveTokens, SafetyMarginTokens: margin,
		RequiredTokens:       inputTokens + request.OutputReserveTokens + margin,
		AvailableInputTokens: available, RemainingInputTokens: remaining, OverflowTokens: overflow,
		UtilizationBasisPoints:      inputTokens * 10_000 / available,
		WarningThresholdBasisPoints: 8_000, CriticalThresholdBasisPoints: 9_500,
		Pressure: pressure, Fits: overflow == 0,
	}
}
