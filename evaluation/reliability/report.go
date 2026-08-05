// Package reliability runs deterministic fault scenarios against the public
// Tiggy Agent Runtime API and produces a versioned reliability report.
package reliability

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const SchemaV1 = "tiggy.runtime.reliability.v1"

type Report struct {
	SchemaVersion string           `json:"schema_version"`
	GeneratedAt   time.Time        `json:"generated_at"`
	Summary       Summary          `json:"summary"`
	Scenarios     []ScenarioResult `json:"scenarios"`
}

type Summary struct {
	Total                    int `json:"total"`
	Passed                   int `json:"passed"`
	Failed                   int `json:"failed"`
	TerminalInvariantPassed  int `json:"terminal_invariant_passed"`
	StateInvariantPassed     int `json:"state_invariant_passed"`
	ClassificationPassed     int `json:"classification_passed"`
	DiagnosticsPassed        int `json:"diagnostics_passed"`
	NoImplicitReplayPassed   int `json:"no_implicit_replay_passed"`
	ImplicitReplayViolations int `json:"implicit_replay_violations"`
}

type ScenarioResult struct {
	ID                   string         `json:"id"`
	Category             string         `json:"category"`
	Fault                string         `json:"fault"`
	Expected             Outcome        `json:"expected"`
	Observed             Outcome        `json:"observed"`
	Checks               Checks         `json:"checks"`
	DelegateCalls        map[string]int `json:"delegate_calls,omitempty"`
	ExpectedMaxDelegates map[string]int `json:"expected_max_delegates,omitempty"`
	DurationMicros       int64          `json:"duration_micros"`
	Passed               bool           `json:"passed"`
	Failure              string         `json:"failure,omitempty"`
}

type Outcome struct {
	Status     string `json:"status"`
	ErrorClass string `json:"error_class,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
}

type Checks struct {
	TerminalInvariant bool `json:"terminal_invariant"`
	StateInvariant    bool `json:"state_invariant"`
	Classification    bool `json:"classification"`
	Diagnostics       bool `json:"diagnostics"`
	NoImplicitReplay  bool `json:"no_implicit_replay"`
}

func (r Report) Passed() bool {
	return r.Summary.Total > 0 && r.Summary.Failed == 0 && r.Summary.Passed == r.Summary.Total
}

func MarshalJSONReport(report Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

func Markdown(report Report) string {
	var output strings.Builder
	output.WriteString("# Tiggy Agent Runtime Reliability Baseline\n\n")
	fmt.Fprintf(&output, "- Schema: `%s`\n", report.SchemaVersion)
	fmt.Fprintf(&output, "- Generated: `%s`\n", report.GeneratedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&output, "- Scenarios: `%d` passed / `%d` total\n", report.Summary.Passed, report.Summary.Total)
	fmt.Fprintf(&output, "- Implicit replay violations: `%d`\n\n", report.Summary.ImplicitReplayViolations)
	output.WriteString("| Scenario | Category | Expected | Observed | Terminal | State | Class | Diagnosis | No replay | Result |\n")
	output.WriteString("|---|---|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, scenario := range report.Scenarios {
		fmt.Fprintf(&output, "| `%s` | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			scenario.ID, escapeTable(scenario.Category), formatOutcome(scenario.Expected), formatOutcome(scenario.Observed),
			mark(scenario.Checks.TerminalInvariant), mark(scenario.Checks.StateInvariant), mark(scenario.Checks.Classification),
			mark(scenario.Checks.Diagnostics), mark(scenario.Checks.NoImplicitReplay), mark(scenario.Passed))
	}
	output.WriteString("\n## Metrics\n\n")
	fmt.Fprintf(&output, "- Terminal invariant: `%d/%d`\n", report.Summary.TerminalInvariantPassed, report.Summary.Total)
	fmt.Fprintf(&output, "- State invariant: `%d/%d`\n", report.Summary.StateInvariantPassed, report.Summary.Total)
	fmt.Fprintf(&output, "- Error classification: `%d/%d`\n", report.Summary.ClassificationPassed, report.Summary.Total)
	fmt.Fprintf(&output, "- Diagnostic evidence: `%d/%d`\n", report.Summary.DiagnosticsPassed, report.Summary.Total)
	fmt.Fprintf(&output, "- No implicit replay: `%d/%d`\n", report.Summary.NoImplicitReplayPassed, report.Summary.Total)
	failed := make([]ScenarioResult, 0)
	for _, scenario := range report.Scenarios {
		if !scenario.Passed {
			failed = append(failed, scenario)
		}
	}
	if len(failed) > 0 {
		output.WriteString("\n## Failures\n\n")
		for _, scenario := range failed {
			fmt.Fprintf(&output, "- `%s`: %s\n", scenario.ID, scenario.Failure)
		}
	}
	return output.String()
}

func summarize(scenarios []ScenarioResult) Summary {
	summary := Summary{Total: len(scenarios)}
	for _, scenario := range scenarios {
		if scenario.Passed {
			summary.Passed++
		} else {
			summary.Failed++
		}
		if scenario.Checks.TerminalInvariant {
			summary.TerminalInvariantPassed++
		}
		if scenario.Checks.StateInvariant {
			summary.StateInvariantPassed++
		}
		if scenario.Checks.Classification {
			summary.ClassificationPassed++
		}
		if scenario.Checks.Diagnostics {
			summary.DiagnosticsPassed++
		}
		if scenario.Checks.NoImplicitReplay {
			summary.NoImplicitReplayPassed++
		} else {
			summary.ImplicitReplayViolations++
		}
	}
	return summary
}

func cloneCounts(values map[string]int) map[string]int {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func countKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func formatOutcome(outcome Outcome) string {
	value := outcome.Status
	if outcome.ErrorClass != "" {
		value += " " + outcome.ErrorClass + "/" + outcome.ErrorCode
	}
	return "`" + escapeTable(value) + "`"
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
