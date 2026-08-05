// Command tiggy-runtime-eval runs deterministic Runtime reliability scenarios
// and writes machine-readable and human-readable baseline reports.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/uyshero/tiggy-agent-runtime/evaluation/reliability"
)

func main() {
	os.Exit(run())
}

func run() int {
	jsonPath := flag.String("json", "reports/reliability-baseline.json", "path for the JSON report")
	markdownPath := flag.String("markdown", "reports/reliability-baseline.md", "path for the Markdown report")
	flag.Parse()

	report := reliability.Run(context.Background())
	jsonReport, err := reliability.MarshalJSONReport(report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode reliability report: %v\n", err)
		return 2
	}
	jsonReport = append(jsonReport, '\n')
	if err := writeFile(*jsonPath, jsonReport); err != nil {
		fmt.Fprintf(os.Stderr, "write JSON reliability report: %v\n", err)
		return 2
	}
	if err := writeFile(*markdownPath, []byte(reliability.Markdown(report))); err != nil {
		fmt.Fprintf(os.Stderr, "write Markdown reliability report: %v\n", err)
		return 2
	}

	fmt.Printf("reliability baseline: %d/%d scenarios passed\n", report.Summary.Passed, report.Summary.Total)
	fmt.Printf("JSON: %s\nMarkdown: %s\n", *jsonPath, *markdownPath)
	if !report.Passed() {
		for _, result := range report.Scenarios {
			if !result.Passed {
				fmt.Fprintf(os.Stderr, "%s: %s\n", result.ID, result.Failure)
			}
		}
		return 1
	}
	return 0
}

func writeFile(path string, content []byte) error {
	if path == "" {
		return fmt.Errorf("output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}
