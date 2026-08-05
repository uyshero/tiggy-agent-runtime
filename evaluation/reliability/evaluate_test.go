package reliability

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunReliabilityBaseline(t *testing.T) {
	t.Parallel()

	report := Run(context.Background())
	if report.SchemaVersion != SchemaV1 {
		t.Fatalf("schema = %q", report.SchemaVersion)
	}
	if report.Summary.Total != 13 {
		t.Fatalf("scenario total = %d, want 13", report.Summary.Total)
	}
	if !report.Passed() {
		for _, result := range report.Scenarios {
			if !result.Passed {
				t.Logf("failed scenario %s: %s", result.ID, result.Failure)
			}
		}
		t.Fatalf("reliability baseline failed: %#v", report.Summary)
	}
	if report.Summary.ImplicitReplayViolations != 0 {
		t.Fatalf("implicit replay violations = %d", report.Summary.ImplicitReplayViolations)
	}
}

func TestReportRendering(t *testing.T) {
	t.Parallel()

	report := Report{
		SchemaVersion: SchemaV1,
		GeneratedAt:   time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
		Scenarios: []ScenarioResult{{
			ID: "control", Category: "control", Expected: Outcome{Status: "completed"}, Observed: Outcome{Status: "completed"},
			Checks: Checks{TerminalInvariant: true, StateInvariant: true, Classification: true, Diagnostics: true, NoImplicitReplay: true},
			Passed: true,
		}},
	}
	report.Summary = summarize(report.Scenarios)

	encoded, err := MarshalJSONReport(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"schema_version": "`+SchemaV1+`"`) {
		t.Fatalf("JSON report = %s", encoded)
	}
	markdown := Markdown(report)
	for _, required := range []string{"# Tiggy Agent Runtime Reliability Baseline", "`control`", "1/1", "PASS"} {
		if !strings.Contains(markdown, required) {
			t.Fatalf("Markdown report does not contain %q:\n%s", required, markdown)
		}
	}
}

func TestNoImplicitReplay(t *testing.T) {
	t.Parallel()

	if !noImplicitReplay(map[string]int{"model": 1, "tool": 1}, map[string]int{"model": 1, "tool": 1}) {
		t.Fatal("calls within the declared maximum were rejected")
	}
	if noImplicitReplay(map[string]int{"model": 2, "tool": 1}, map[string]int{"model": 1, "tool": 1}) {
		t.Fatal("model replay above the declared maximum was accepted")
	}
	if noImplicitReplay(map[string]int{"model": 1, "unexpected": 1}, map[string]int{"model": 1}) {
		t.Fatal("an undeclared delegate call was accepted")
	}
}
