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
	logger *Logger
}

// NewProgressReporter creates a new progress reporter
func NewProgressReporter(format OutputFormat, logger *Logger) *ProgressReporter {
	return &ProgressReporter{
		format: format,
		logger: logger,
	}
}

// ReportState reports the current test state
func (pr *ProgressReporter) ReportState(state LiveTestState) {
	switch pr.format {
	case FormatJSON:
		pr.reportJSON(state)
	case FormatTUI:
		pr.reportTUI(state)
	default:
		pr.reportText(state)
	}
}

// ReportStateTransition reports a state transition
func (pr *ProgressReporter) ReportStateTransition(from, to string) {
	switch pr.format {
	case FormatJSON:
		data, _ := json.Marshal(map[string]interface{}{
			"event":      "state_transition",
			"from_state": from,
			"to_state":   to,
			"timestamp":  time.Now(),
		})
		fmt.Println(string(data))
	case FormatTUI:
		pr.clearLine()
		fmt.Printf("🔄 State Transition: %s → %s\n", from, to)
	default:
		fmt.Printf("[STATE] %s → %s\n", from, to)
	}
}

// ReportFaultInjection reports fault injection
func (pr *ProgressReporter) ReportFaultInjection(fault FaultInfo) {
	switch pr.format {
	case FormatJSON:
		data, _ := json.Marshal(map[string]interface{}{
			"event":       "fault_injection",
			"fault":       fault,
			"timestamp":   time.Now(),
		})
		fmt.Println(string(data))
	case FormatTUI:
		pr.clearLine()
		fmt.Printf("⚡ Injecting Fault: %s on %s\n", fault.Phase, fault.Target)
		if fault.Description != "" {
			fmt.Printf("   %s\n", fault.Description)
		}
	default:
		fmt.Printf("[FAULT] %s: %s on %s\n", fault.Phase, fault.Type, fault.Target)
	}
}

// ReportCriterionEvaluation reports success criterion evaluation
func (pr *ProgressReporter) ReportCriterionEvaluation(result CriterionResult) {
	status := "✅ PASS"
	if !result.Passed {
		status = "❌ FAIL"
		if result.Critical {
			status = "🔴 CRITICAL FAIL"
		}
	}

	switch pr.format {
	case FormatJSON:
		data, _ := json.Marshal(map[string]interface{}{
			"event":     "criterion_evaluation",
			"result":    result,
			"timestamp": time.Now(),
		})
		fmt.Println(string(data))
	case FormatTUI:
		pr.clearLine()
		fmt.Printf("%s %s: %s\n", status, result.Name, result.Message)
		if result.Query != "" {
			fmt.Printf("   Query: %s\n", result.Query)
			fmt.Printf("   Value: %.2f, Threshold: %s\n", result.Value, result.Threshold)
		}
	default:
		fmt.Printf("[CRITERION] %s %s: %s\n", status, result.Name, result.Message)
	}
}

// ReportCleanupStarted reports cleanup started
func (pr *ProgressReporter) ReportCleanupStarted() {
	switch pr.format {
	case FormatJSON:
		data, _ := json.Marshal(map[string]interface{}{
			"event":     "cleanup_started",
			"timestamp": time.Now(),
		})
		fmt.Println(string(data))
	case FormatTUI:
		pr.clearLine()
		fmt.Println("🧹 Starting cleanup...")
	default:
		fmt.Println("[CLEANUP] Starting cleanup...")
	}
}

// ReportCleanupCompleted reports cleanup completed
func (pr *ProgressReporter) ReportCleanupCompleted(succeeded, failed int) {
	switch pr.format {
	case FormatJSON:
		data, _ := json.Marshal(map[string]interface{}{
			"event":     "cleanup_completed",
			"succeeded": succeeded,
			"failed":    failed,
			"timestamp": time.Now(),
		})
		fmt.Println(string(data))
	case FormatTUI:
		pr.clearLine()
		fmt.Printf("🧹 Cleanup complete: %d succeeded, %d failed\n", succeeded, failed)
	default:
		fmt.Printf("[CLEANUP] Complete: %d succeeded, %d failed\n", succeeded, failed)
	}
}

// ReportTestCompleted reports test completion
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
		pr.printTestSummary(report)
	default:
		pr.printTextSummary(report)
	}
}

// reportText outputs progress in plain text format
func (pr *ProgressReporter) reportText(state LiveTestState) {
	elapsed := time.Since(state.StartTime).Round(time.Second)
	fmt.Printf("[%s] %s | Elapsed: %s\n",
		time.Now().Format("15:04:05"),
		state.State,
		elapsed,
	)

	if len(state.ActiveFaults) > 0 {
		fmt.Printf("  Active Faults: %d\n", len(state.ActiveFaults))
	}

	if len(state.LatestMetrics) > 0 {
		fmt.Printf("  Metrics: ")
		for name, value := range state.LatestMetrics {
			fmt.Printf("%s=%.2f ", name, value)
		}
		fmt.Println()
	}
}

// reportJSON outputs progress in JSON format
func (pr *ProgressReporter) reportJSON(state LiveTestState) {
	data, err := json.Marshal(state)
	if err != nil {
		pr.logger.Error("Failed to marshal state", "error", err)
		return
	}
	fmt.Println(string(data))
}

// reportTUI outputs progress in terminal UI format
func (pr *ProgressReporter) reportTUI(state LiveTestState) {
	pr.clearScreen()

	// Header
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("   Chaos Test: %s\n", state.ScenarioName)
	fmt.Printf("   Test ID: %s\n", state.TestID)
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println()

	// Status
	fmt.Printf("📊 State: %s\n", state.State)
	fmt.Printf("⏱️  Elapsed: %s\n", state.Elapsed.Round(time.Second))
	fmt.Println()

	// Active faults
	if len(state.ActiveFaults) > 0 {
		fmt.Printf("⚡ Active Faults (%d):\n", len(state.ActiveFaults))
		for _, fault := range state.ActiveFaults {
			faultElapsed := time.Since(fault.StartTime).Round(time.Second)
			fmt.Printf("   • %s on %s (running: %s)\n", fault.Phase, fault.Target, faultElapsed)
		}
		fmt.Println()
	}

	// Latest metrics
	if len(state.LatestMetrics) > 0 {
		fmt.Printf("📈 Latest Metrics:\n")
		for name, value := range state.LatestMetrics {
			fmt.Printf("   • %s: %.2f\n", name, value)
		}
		fmt.Println()
	}

	// Success criteria
	if len(state.CriteriaStatus) > 0 {
		fmt.Printf("✅ Success Criteria:\n")
		for _, criterion := range state.CriteriaStatus {
			status := "✅"
			if !criterion.Passed {
				status = "❌"
				if criterion.Critical {
					status = "🔴"
				}
			}
			fmt.Printf("   %s %s: %s\n", status, criterion.Name, criterion.Message)
		}
		fmt.Println()
	}

	fmt.Println(strings.Repeat("─", 80))
}

// printTestSummary prints a test summary in TUI format
func (pr *ProgressReporter) printTestSummary(report *TestReport) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("   TEST SUMMARY")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println()

	// Status
	statusIcon := "✅"
	statusText := "PASSED"
	if !report.Success {
		statusIcon = "❌"
		statusText = "FAILED"
	}
	if report.Status == StatusStopped {
		statusIcon = "🛑"
		statusText = "STOPPED"
	}

	fmt.Printf("%s Test %s\n", statusIcon, statusText)
	fmt.Printf("   Scenario: %s\n", report.ScenarioName)
	fmt.Printf("   Test ID: %s\n", report.TestID)
	fmt.Printf("   Duration: %s\n", report.Duration)
	fmt.Println()

	// Targets
	if len(report.Targets) > 0 {
		fmt.Printf("🎯 Targets (%d):\n", len(report.Targets))
		for _, target := range report.Targets {
			fmt.Printf("   • %s (%s)\n", target.Alias, target.ServiceName)
		}
		fmt.Println()
	}

	// Faults
	if len(report.Faults) > 0 {
		fmt.Printf("⚡ Faults Injected (%d):\n", len(report.Faults))
		for _, fault := range report.Faults {
			fmt.Printf("   • %s: %s on %s\n", fault.Phase, fault.Type, fault.Target)
		}
		fmt.Println()
	}

	// Success criteria
	if len(report.SuccessCriteria) > 0 {
		passed := 0
		failedCritical := 0
		failedNonCritical := 0
		for _, criterion := range report.SuccessCriteria {
			if criterion.Passed {
				passed++
			} else {
				if criterion.Critical {
					failedCritical++
				} else {
					failedNonCritical++
				}
			}
		}

		statusIcon := "✅"
		statusText := fmt.Sprintf("%d passed", passed)
		if failedCritical > 0 {
			statusIcon = "🔴"
			statusText += fmt.Sprintf(", %d critical failed", failedCritical)
		}
		if failedNonCritical > 0 {
			if failedCritical == 0 {
				statusIcon = "⚠️"
			}
			statusText += fmt.Sprintf(", %d non-critical failed", failedNonCritical)
		}
		fmt.Printf("%s Success Criteria: %s\n", statusIcon, statusText)
		for _, criterion := range report.SuccessCriteria {
			status := "✅"
			if !criterion.Passed {
				status = "❌"
				if criterion.Critical {
					status = "🔴"
				}
			}
			fmt.Printf("   %s %s: %s\n", status, criterion.Name, criterion.Message)
		}
		fmt.Println()
	}

	// Cleanup
	fmt.Printf("🧹 Cleanup: %d succeeded, %d failed\n",
		report.CleanupSummary.Succeeded,
		report.CleanupSummary.Failed,
	)
	fmt.Println()

	fmt.Println(strings.Repeat("=", 80))
}

// printTextSummary prints a test summary in plain text format
func (pr *ProgressReporter) printTextSummary(report *TestReport) {
	status := "PASSED"
	if !report.Success {
		status = "FAILED"
	}
	if report.Status == StatusStopped {
		status = "STOPPED"
	}

	fmt.Printf("\n[TEST SUMMARY] %s\n", status)
	fmt.Printf("  Scenario: %s\n", report.ScenarioName)
	fmt.Printf("  Test ID: %s\n", report.TestID)
	fmt.Printf("  Duration: %s\n", report.Duration)
	fmt.Printf("  Targets: %d\n", len(report.Targets))
	fmt.Printf("  Faults: %d\n", len(report.Faults))

	if len(report.SuccessCriteria) > 0 {
		passed := 0
		failedCritical := 0
		failedNonCritical := 0
		for _, criterion := range report.SuccessCriteria {
			if criterion.Passed {
				passed++
			} else {
				if criterion.Critical {
					failedCritical++
				} else {
					failedNonCritical++
				}
			}
		}
		fmt.Printf("  Success Criteria: %d/%d passed", passed, len(report.SuccessCriteria))
		if failedCritical > 0 {
			fmt.Printf(", %d critical failed", failedCritical)
		}
		if failedNonCritical > 0 {
			fmt.Printf(", %d non-critical failed", failedNonCritical)
		}
		fmt.Println()
	}

	fmt.Printf("  Cleanup: %d succeeded, %d failed\n",
		report.CleanupSummary.Succeeded,
		report.CleanupSummary.Failed,
	)
	fmt.Println()
}

// clearScreen clears the terminal screen
func (pr *ProgressReporter) clearScreen() {
	// ANSI escape code to clear screen and move cursor to top
	fmt.Print("\033[2J\033[H")
}

// clearLine clears the current line
func (pr *ProgressReporter) clearLine() {
	// ANSI escape code to clear current line
	fmt.Print("\033[K")
}
