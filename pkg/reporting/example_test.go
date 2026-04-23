package reporting_test

import (
	"fmt"
	"os"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/core/cleanup"
	"github.com/jihwankim/chaos-utils/pkg/reporting"
)

// Example demonstrates the reporting package usage
func Example() {
	// Create logger
	logger := reporting.NewLogger(reporting.LoggerConfig{
		Level:  reporting.LogLevelInfo,
		Format: reporting.LogFormatText,
		Output: os.Stdout,
	})

	logger.Info("Chaos test starting")
	logger.Info("Target discovered", "service", "validator-1", "ip", "10.0.0.5")
	logger.Info("Fault injected", "type", "network", "target", "validator-1")

	// Create storage
	storage, err := reporting.NewStorage("./test-reports", 10, logger)
	if err != nil {
		fmt.Printf("Failed to create storage: %v\n", err)
		return
	}
	defer os.RemoveAll("./test-reports")

	// Create test report
	report := &reporting.TestReport{
		TestID:       "test-12345",
		ScenarioName: "validator-partition",
		StartTime:    time.Now().Add(-5 * time.Minute),
		EndTime:      time.Now(),
		Duration:     "5m0s",
		Status:       reporting.StatusCompleted,
		Success:      true,
		Targets: []reporting.TargetInfo{
			{
				Alias:       "victim_validator",
				ServiceName: "l2-cl-1-heimdall-v2-validator",
				ContainerID: "abc123",
			},
		},
		Faults: []reporting.FaultInfo{
			{
				Phase:     "partition_consensus",
				Type:      "network",
				Target:    "victim_validator",
				StartTime: time.Now().Add(-4 * time.Minute),
				EndTime:   time.Now().Add(-1 * time.Minute),
				Duration:  "3m0s",
			},
		},
		SuccessCriteria: []reporting.CriterionResult{
			{
				Name:      "network_continues",
				Type:      "prometheus",
				Passed:    true,
				Value:     1.5,
				Threshold: "> 0",
				Message:   "Block production continuing",
			},
		},
		CleanupSummary: cleanup.CleanupSummary{
			TotalActions: 3,
			Succeeded:    3,
			Failed:       0,
		},
	}

	// Save report
	path, err := storage.SaveReport(report)
	if err != nil {
		fmt.Printf("Failed to save report: %v\n", err)
		return
	}

	fmt.Printf("Report saved successfully\n")

	// List reports
	summaries, err := storage.ListReports()
	if err != nil {
		fmt.Printf("Failed to list reports: %v\n", err)
		return
	}

	fmt.Printf("Found %d report(s)\n", len(summaries))
	for _, summary := range summaries {
		fmt.Printf("  %s: %s (%s)\n", summary.TestID, summary.ScenarioName, summary.Status)
	}

	// Load report
	loadedReport, err := storage.LoadReport(path)
	if err != nil {
		fmt.Printf("Failed to load report: %v\n", err)
		return
	}

	fmt.Printf("Loaded report for test: %s\n", loadedReport.TestID)

	// Create formatter
	formatter := reporting.NewFormatter(logger)

	// Generate text report
	textPath := "./test-reports/report.txt"
	if err := formatter.GenerateReport(report, reporting.ReportFormatText, textPath); err != nil {
		fmt.Printf("Failed to generate text report: %v\n", err)
		return
	}
	fmt.Printf("Text report generated\n")

	// Generate HTML report
	htmlPath := "./test-reports/report.html"
	if err := formatter.GenerateReport(report, reporting.ReportFormatHTML, htmlPath); err != nil {
		fmt.Printf("Failed to generate HTML report: %v\n", err)
		return
	}
	fmt.Printf("HTML report generated\n")

	// Output will vary due to timestamps, so we don't include it
}
