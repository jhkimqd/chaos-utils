package reporting

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// OutputFormat represents the progress output format
type OutputFormat string

const (
	FormatText OutputFormat = "text"
	FormatJSON OutputFormat = "json"
	FormatTUI  OutputFormat = "tui"
)

// ProgressReporter reports test execution progress
type ProgressReporter struct {
	format OutputFormat
}

// NewProgressReporter creates a new progress reporter. The logger parameter is
// accepted for call-site compatibility; the reporter writes directly to stdout.
func NewProgressReporter(format OutputFormat, _ *Logger) *ProgressReporter {
	return &ProgressReporter{format: format}
}

// ReportTestCompleted reports test completion in the configured format.
func (pr *ProgressReporter) ReportTestCompleted(report *TestReport) {
	switch pr.format {
	case FormatJSON:
		data, _ := json.Marshal(map[string]interface{}{
			"event":     "test_completed",
			"report":    report,
			"timestamp": time.Now(),
		})
		fmt.Println(string(data))
	case FormatTUI:
		pr.clearLine()
		pr.printSummary(report)
	default:
		pr.printSummary(report)
	}
}

// printSummary renders the end-of-test summary.
func (pr *ProgressReporter) printSummary(report *TestReport) {
	w := 72

	fmt.Println()
	fmt.Println(strings.Repeat("═", w))

	// Overall verdict banner
	if report.Status == StatusStopped {
		fmt.Printf("  STOPPED  %s\n", report.ScenarioName)
	} else if report.Success {
		fmt.Printf("  ✓ PASSED  %s\n", report.ScenarioName)
	} else {
		fmt.Printf("  ✗ FAILED  %s\n", report.ScenarioName)
	}

	fmt.Println(strings.Repeat("═", w))
	fmt.Printf("  Test ID:   %s\n", report.TestID)
	fmt.Printf("  Duration:  %s\n", report.Duration)
	fmt.Printf("  Targets:   %d\n", len(report.Targets))
	// Show spec count and install count. Installs = the number of
	// (container, faultType) pairs orchestrator actually injected, which
	// differs from spec count for compound scenarios and multi-container
	// targets. Equal-when-simple so the extra column stays quiet.
	if report.FaultInstalls > len(report.Faults) {
		fmt.Printf("  Faults:    %d specs, %d installs\n", len(report.Faults), report.FaultInstalls)
	} else {
		fmt.Printf("  Faults:    %d\n", len(report.Faults))
	}
	fmt.Println(strings.Repeat("─", w))

	// Success criteria results
	if len(report.SuccessCriteria) > 0 {
		passed := 0
		var failedCriteria []CriterionResult
		for _, c := range report.SuccessCriteria {
			if c.Passed {
				passed++
			} else {
				failedCriteria = append(failedCriteria, c)
			}
		}

		fmt.Printf("  Success Criteria: %d/%d passed\n", passed, len(report.SuccessCriteria))
		fmt.Println()

		for _, c := range report.SuccessCriteria {
			if c.Passed {
				fmt.Printf("    ✓  %s\n", c.Name)
			} else if c.Critical {
				fmt.Printf("    ✗  %s  (CRITICAL)\n", c.Name)
			} else {
				fmt.Printf("    ✗  %s  (non-critical)\n", c.Name)
			}
		}

		// Detailed failure section
		if len(failedCriteria) > 0 {
			fmt.Println()
			fmt.Println(strings.Repeat("─", w))
			fmt.Println("  FAILED CRITERIA DETAILS")
			fmt.Println(strings.Repeat("─", w))
			for _, c := range failedCriteria {
				severity := "non-critical"
				if c.Critical {
					severity = "CRITICAL"
				}
				fmt.Printf("\n    ✗ %s [%s]\n", c.Name, severity)
				if c.Description != "" {
					fmt.Printf("      %s\n", c.Description)
				}
				fmt.Printf("      got %.4g, expected %s\n", c.Value, c.Threshold)
				if c.Query != "" {
					fmt.Printf("      query: %s\n", c.Query)
				}
				// Surface the detector's Message so diagnostic states like
				// "no targets available for log scanning" (e.g. when a log
				// criterion's target container was killed) aren't hidden
				// behind a misleading "got 0, expected" line.
				if c.Message != "" {
					fmt.Printf("      reason: %s\n", c.Message)
				}
			}
		}
	} else {
		fmt.Println("  No success criteria defined")
	}

	// Cleanup
	fmt.Println()
	fmt.Println(strings.Repeat("─", w))
	fmt.Printf("  Cleanup: %d succeeded, %d failed\n",
		report.CleanupSummary.Succeeded,
		report.CleanupSummary.Failed,
	)

	// Errors
	if len(report.Errors) > 0 {
		fmt.Println()
		fmt.Println(strings.Repeat("─", w))
		fmt.Println("  ERRORS")
		fmt.Println(strings.Repeat("─", w))
		for _, e := range report.Errors {
			fmt.Printf("    • %s\n", e)
		}
	}

	fmt.Println(strings.Repeat("═", w))
	fmt.Println()
}

// clearLine clears the current terminal line via ANSI escape.
func (pr *ProgressReporter) clearLine() {
	fmt.Print("\033[K")
}
