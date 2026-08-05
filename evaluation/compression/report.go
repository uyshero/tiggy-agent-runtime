// Package compression evaluates caller-owned Context Compressors with
// deterministic, application-defined fixtures.
package compression

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const SchemaV1 = "tiggy.compression.quality.v1"

type Report struct {
	SchemaVersion string       `json:"schema_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Summary       Summary      `json:"summary"`
	Cases         []CaseResult `json:"cases"`
}

type Summary struct {
	Total                     int `json:"total"`
	Passed                    int `json:"passed"`
	Failed                    int `json:"failed"`
	DecisionValidPassed       int `json:"decision_valid_passed"`
	StructuralIntegrityPassed int `json:"structural_integrity_passed"`
	ProtectedMessagesPassed   int `json:"protected_messages_passed"`
	ProvenancePassed          int `json:"provenance_passed"`
	RequiredFactsPassed       int `json:"required_facts_passed"`
	SummaryBoundPassed        int `json:"summary_bound_passed"`
	CompressedBudgetFitPassed int `json:"compressed_budget_fit_passed"`
}

type CaseResult struct {
	ID                string       `json:"id"`
	StrategyName      string       `json:"strategy_name,omitempty"`
	StrategyVersion   string       `json:"strategy_version,omitempty"`
	BeforeInputTokens int64        `json:"before_input_tokens,omitempty"`
	AfterInputTokens  int64        `json:"after_input_tokens,omitempty"`
	SummaryBytes      int          `json:"summary_bytes,omitempty"`
	CoveredGroups     int          `json:"covered_groups,omitempty"`
	CoveredMessages   int          `json:"covered_messages,omitempty"`
	Checks            Checks       `json:"checks"`
	Facts             []FactResult `json:"facts,omitempty"`
	DurationMicros    int64        `json:"duration_micros"`
	Passed            bool         `json:"passed"`
	FailureCode       string       `json:"failure_code,omitempty"`
	Failure           string       `json:"failure,omitempty"`
}

type Checks struct {
	DecisionValid              bool `json:"decision_valid"`
	StructuralIntegrity        bool `json:"structural_integrity"`
	ProtectedMessagesPreserved bool `json:"protected_messages_preserved"`
	ProvenanceExact            bool `json:"provenance_exact"`
	RequiredFactsPreserved     bool `json:"required_facts_preserved"`
	SummaryBounded             bool `json:"summary_bounded"`
	CompressedBudgetFits       bool `json:"compressed_budget_fits"`
}

type FactResult struct {
	ID             string `json:"id"`
	SourcesCovered bool   `json:"sources_covered"`
	Matched        bool   `json:"matched"`
	Passed         bool   `json:"passed"`
}

func (r Report) Passed() bool {
	return r.Summary.Total > 0 && r.Summary.Failed == 0 && r.Summary.Passed == r.Summary.Total
}

func MarshalJSONReport(report Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

func Markdown(report Report) string {
	var output strings.Builder
	output.WriteString("# Tiggy Compressor Quality Report\n\n")
	fmt.Fprintf(&output, "- Schema: `%s`\n", report.SchemaVersion)
	fmt.Fprintf(&output, "- Generated: `%s`\n", report.GeneratedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&output, "- Cases: `%d` passed / `%d` total\n\n", report.Summary.Passed, report.Summary.Total)
	output.WriteString("| Case | Strategy | Before | After | Facts | Structure | Protected | Provenance | Bound | Fits | Result |\n")
	output.WriteString("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, result := range report.Cases {
		strategy := result.StrategyName
		if result.StrategyVersion != "" {
			strategy += "@" + result.StrategyVersion
		}
		fmt.Fprintf(&output, "| `%s` | `%s` | %d | %d | %s | %s | %s | %s | %s | %s | %s |\n",
			escapeTable(result.ID), escapeTable(strategy), result.BeforeInputTokens, result.AfterInputTokens,
			mark(result.Checks.RequiredFactsPreserved), mark(result.Checks.StructuralIntegrity),
			mark(result.Checks.ProtectedMessagesPreserved), mark(result.Checks.ProvenanceExact),
			mark(result.Checks.SummaryBounded), mark(result.Checks.CompressedBudgetFits), mark(result.Passed))
	}
	output.WriteString("\n## Metrics\n\n")
	fmt.Fprintf(&output, "- Valid Decision: `%d/%d`\n", report.Summary.DecisionValidPassed, report.Summary.Total)
	fmt.Fprintf(&output, "- Structural integrity: `%d/%d`\n", report.Summary.StructuralIntegrityPassed, report.Summary.Total)
	fmt.Fprintf(&output, "- Protected Messages: `%d/%d`\n", report.Summary.ProtectedMessagesPassed, report.Summary.Total)
	fmt.Fprintf(&output, "- Exact provenance: `%d/%d`\n", report.Summary.ProvenancePassed, report.Summary.Total)
	fmt.Fprintf(&output, "- Required facts: `%d/%d`\n", report.Summary.RequiredFactsPassed, report.Summary.Total)
	fmt.Fprintf(&output, "- Summary bound: `%d/%d`\n", report.Summary.SummaryBoundPassed, report.Summary.Total)
	fmt.Fprintf(&output, "- Compressed budget fit: `%d/%d`\n", report.Summary.CompressedBudgetFitPassed, report.Summary.Total)
	for _, result := range report.Cases {
		if !result.Passed {
			fmt.Fprintf(&output, "\n- `%s`: %s\n", escapeTable(result.ID), result.Failure)
		}
	}
	return output.String()
}

func summarize(results []CaseResult) Summary {
	summary := Summary{Total: len(results)}
	for _, result := range results {
		if result.Passed {
			summary.Passed++
		} else {
			summary.Failed++
		}
		if result.Checks.DecisionValid {
			summary.DecisionValidPassed++
		}
		if result.Checks.StructuralIntegrity {
			summary.StructuralIntegrityPassed++
		}
		if result.Checks.ProtectedMessagesPreserved {
			summary.ProtectedMessagesPassed++
		}
		if result.Checks.ProvenanceExact {
			summary.ProvenancePassed++
		}
		if result.Checks.RequiredFactsPreserved {
			summary.RequiredFactsPassed++
		}
		if result.Checks.SummaryBounded {
			summary.SummaryBoundPassed++
		}
		if result.Checks.CompressedBudgetFits {
			summary.CompressedBudgetFitPassed++
		}
	}
	return summary
}

func mark(value bool) string {
	if value {
		return "PASS"
	}
	return "FAIL"
}

func escapeTable(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}
