package reporting

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReportFormat represents the report output format
type ReportFormat string

const (
	ReportFormatHTML ReportFormat = "html"
	ReportFormatText ReportFormat = "text"
	ReportFormatJSON ReportFormat = "json"
)

// Formatter generates formatted reports from test data
type Formatter struct {
	logger *Logger
}

// NewFormatter creates a new report formatter
func NewFormatter(logger *Logger) *Formatter {
	return &Formatter{
		logger: logger,
	}
}

// GenerateReport generates a report in the specified format
func (f *Formatter) GenerateReport(report *TestReport, format ReportFormat, outputPath string) error {
	switch format {
	case ReportFormatHTML:
		return f.generateHTMLReport(report, outputPath)
	case ReportFormatText:
		return f.generateTextReport(report, outputPath)
	case ReportFormatJSON:
		// Already handled by storage
		return fmt.Errorf("JSON format is automatically saved by storage")
	default:
		return fmt.Errorf("unsupported report format: %s", format)
	}
}

// generateHTMLReport generates an HTML report
func (f *Formatter) generateHTMLReport(report *TestReport, outputPath string) error {
	tmpl, err := template.New("report").Funcs(template.FuncMap{
		"formatTime": func(t time.Time) string {
			return t.Format("2006-01-02 15:04:05")
		},
		"statusClass": func(passed bool) string {
			if passed {
				return "pass"
			}
			return "fail"
		},
		"statusIcon": func(passed bool) string {
			if passed {
				return "✅"
			}
			return "❌"
		},
	}).Parse(htmlTemplate)

	if err != nil {
		return fmt.Errorf("failed to parse HTML template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, report); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write HTML report: %w", err)
	}

	f.logger.Info("HTML report generated", "path", outputPath)
	return nil
}

// generateTextReport generates a plain text report
func (f *Formatter) generateTextReport(report *TestReport, outputPath string) error {
	var buf bytes.Buffer

	// Header
	buf.WriteString(strings.Repeat("=", 80) + "\n")
	buf.WriteString("   CHAOS TEST REPORT\n")
	buf.WriteString(strings.Repeat("=", 80) + "\n\n")

	// Test Summary
	status := "PASSED"
	if !report.Success {
		status = "FAILED"
	}
	if report.Status == StatusStopped {
		status = "STOPPED"
	}

	buf.WriteString("TEST SUMMARY\n")
	buf.WriteString(strings.Repeat("-", 80) + "\n")
	buf.WriteString(fmt.Sprintf("Status:       %s\n", status))
	buf.WriteString(fmt.Sprintf("Test ID:      %s\n", report.TestID))
	buf.WriteString(fmt.Sprintf("Scenario:     %s\n", report.ScenarioName))
	buf.WriteString(fmt.Sprintf("Start Time:   %s\n", report.StartTime.Format("2006-01-02 15:04:05")))
	buf.WriteString(fmt.Sprintf("End Time:     %s\n", report.EndTime.Format("2006-01-02 15:04:05")))
	buf.WriteString(fmt.Sprintf("Duration:     %s\n", report.Duration))
	if report.Message != "" {
		buf.WriteString(fmt.Sprintf("Message:      %s\n", report.Message))
	}
	buf.WriteString("\n")

	// Targets
	if len(report.Targets) > 0 {
		buf.WriteString("TARGETS\n")
		buf.WriteString(strings.Repeat("-", 80) + "\n")
		for i, target := range report.Targets {
			buf.WriteString(fmt.Sprintf("%d. %s\n", i+1, target.Alias))
			buf.WriteString(fmt.Sprintf("   Service:    %s\n", target.ServiceName))
			buf.WriteString(fmt.Sprintf("   Container:  %s\n", target.ContainerID))
			if target.IP != "" {
				buf.WriteString(fmt.Sprintf("   IP:         %s\n", target.IP))
			}
			if len(target.Ports) > 0 {
				buf.WriteString(fmt.Sprintf("   Ports:      %v\n", target.Ports))
			}
			buf.WriteString("\n")
		}
	}

	// Faults
	if len(report.Faults) > 0 {
		buf.WriteString("FAULTS INJECTED\n")
		buf.WriteString(strings.Repeat("-", 80) + "\n")
		for i, fault := range report.Faults {
			buf.WriteString(fmt.Sprintf("%d. %s\n", i+1, fault.Phase))
			buf.WriteString(fmt.Sprintf("   Type:        %s\n", fault.Type))
			buf.WriteString(fmt.Sprintf("   Target:      %s\n", fault.Target))
			if fault.Description != "" {
				buf.WriteString(fmt.Sprintf("   Description: %s\n", fault.Description))
			}
			buf.WriteString(fmt.Sprintf("   Start Time:  %s\n", fault.StartTime.Format("15:04:05")))
			if !fault.EndTime.IsZero() {
				buf.WriteString(fmt.Sprintf("   End Time:    %s\n", fault.EndTime.Format("15:04:05")))
				buf.WriteString(fmt.Sprintf("   Duration:    %s\n", fault.Duration))
			}
			if len(fault.Parameters) > 0 {
				buf.WriteString(fmt.Sprintf("   Parameters:  %v\n", fault.Parameters))
			}
			buf.WriteString("\n")
		}
	}

	// Success Criteria
	if len(report.SuccessCriteria) > 0 {
		passed := 0
		failed := 0
		for _, criterion := range report.SuccessCriteria {
			if criterion.Passed {
				passed++
			} else {
				failed++
			}
		}

		buf.WriteString("SUCCESS CRITERIA\n")
		buf.WriteString(strings.Repeat("-", 80) + "\n")
		buf.WriteString(fmt.Sprintf("Summary: %d passed, %d failed\n\n", passed, failed))

		for i, criterion := range report.SuccessCriteria {
			status := "PASS"
			if !criterion.Passed {
				status = "FAIL"
				if criterion.Critical {
					status = "CRITICAL FAIL"
				}
			}

			buf.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, status, criterion.Name))
			if criterion.Description != "" {
				buf.WriteString(fmt.Sprintf("   Description: %s\n", criterion.Description))
			}
			buf.WriteString(fmt.Sprintf("   Type:        %s\n", criterion.Type))
			if criterion.Query != "" {
				buf.WriteString(fmt.Sprintf("   Query:       %s\n", criterion.Query))
			}
			if criterion.Threshold != "" {
				buf.WriteString(fmt.Sprintf("   Threshold:   %s\n", criterion.Threshold))
			}
			buf.WriteString(fmt.Sprintf("   Value:       %.2f\n", criterion.Value))
			buf.WriteString(fmt.Sprintf("   Message:     %s\n", criterion.Message))
			buf.WriteString(fmt.Sprintf("   Evaluated:   %s\n", criterion.EvalTime.Format("15:04:05")))
			buf.WriteString("\n")
		}
	}

	// Cleanup Summary
	buf.WriteString("CLEANUP SUMMARY\n")
	buf.WriteString(strings.Repeat("-", 80) + "\n")
	buf.WriteString(fmt.Sprintf("Total Actions: %d\n", report.CleanupSummary.TotalActions))
	buf.WriteString(fmt.Sprintf("Succeeded:     %d\n", report.CleanupSummary.Succeeded))
	buf.WriteString(fmt.Sprintf("Failed:        %d\n", report.CleanupSummary.Failed))
	buf.WriteString("\n")

	// Cleanup Audit Log
	if len(report.CleanupLog) > 0 {
		buf.WriteString("CLEANUP AUDIT LOG\n")
		buf.WriteString(strings.Repeat("-", 80) + "\n")
		for i, entry := range report.CleanupLog {
			status := "✓"
			if !entry.Success {
				status = "✗"
			}
			buf.WriteString(fmt.Sprintf("%d. [%s] %s %s\n",
				i+1,
				entry.Timestamp.Format("15:04:05"),
				status,
				entry.Action,
			))
			buf.WriteString(fmt.Sprintf("   Target:  %s\n", entry.Target))
			buf.WriteString(fmt.Sprintf("   Details: %s\n", entry.Details))
			if entry.Error != nil {
				buf.WriteString(fmt.Sprintf("   Error:   %v\n", entry.Error))
			}
			buf.WriteString("\n")
		}
	}

	// Errors
	if len(report.Errors) > 0 {
		buf.WriteString("ERRORS\n")
		buf.WriteString(strings.Repeat("-", 80) + "\n")
		for i, err := range report.Errors {
			buf.WriteString(fmt.Sprintf("%d. %s\n", i+1, err))
		}
		buf.WriteString("\n")
	}

	// Footer
	buf.WriteString(strings.Repeat("=", 80) + "\n")
	buf.WriteString(fmt.Sprintf("Generated: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	buf.WriteString(strings.Repeat("=", 80) + "\n")

	// Write to file
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write text report: %w", err)
	}

	f.logger.Info("Text report generated", "path", outputPath)
	return nil
}

// CompareReports generates a comparison report for multiple test runs
func (f *Formatter) CompareReports(reports []*TestReport, outputPath string) error {
	if len(reports) < 2 {
		return fmt.Errorf("need at least 2 reports to compare")
	}

	var buf bytes.Buffer

	// Header
	buf.WriteString(strings.Repeat("=", 80) + "\n")
	buf.WriteString("   CHAOS TEST COMPARISON\n")
	buf.WriteString(strings.Repeat("=", 80) + "\n\n")

	// Sort by start time
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].StartTime.Before(reports[j].StartTime)
	})

	// Summary table
	buf.WriteString("TEST SUMMARY\n")
	buf.WriteString(strings.Repeat("-", 80) + "\n")
	buf.WriteString(fmt.Sprintf("%-20s %-15s %-12s %-10s %-10s\n",
		"Test ID", "Scenario", "Status", "Duration", "Passed"))
	buf.WriteString(strings.Repeat("-", 80) + "\n")

	for _, report := range reports {
		status := "PASSED"
		if !report.Success {
			status = "FAILED"
		}
		passed := 0
		total := len(report.SuccessCriteria)
		for _, criterion := range report.SuccessCriteria {
			if criterion.Passed {
				passed++
			}
		}

		buf.WriteString(fmt.Sprintf("%-20s %-15s %-12s %-10s %d/%d\n",
			report.TestID[:min(20, len(report.TestID))],
			report.ScenarioName[:min(15, len(report.ScenarioName))],
			status,
			report.Duration,
			passed,
			total,
		))
	}
	buf.WriteString("\n")

	// Success criteria comparison
	buf.WriteString("SUCCESS CRITERIA COMPARISON\n")
	buf.WriteString(strings.Repeat("-", 80) + "\n")

	// Collect all unique criterion names
	criterionNames := make(map[string]bool)
	for _, report := range reports {
		for _, criterion := range report.SuccessCriteria {
			criterionNames[criterion.Name] = true
		}
	}

	// Sort criterion names
	names := make([]string, 0, len(criterionNames))
	for name := range criterionNames {
		names = append(names, name)
	}
	sort.Strings(names)

	// Print comparison for each criterion
	for _, name := range names {
		buf.WriteString(fmt.Sprintf("\n%s:\n", name))
		for _, report := range reports {
			// Find criterion in this report
			var criterion *CriterionResult
			for i := range report.SuccessCriteria {
				if report.SuccessCriteria[i].Name == name {
					criterion = &report.SuccessCriteria[i]
					break
				}
			}

			if criterion != nil {
				status := "✓"
				if !criterion.Passed {
					status = "✗"
				}
				buf.WriteString(fmt.Sprintf("  %s [%s] %s: %.2f (%s)\n",
					status,
					report.TestID[:min(12, len(report.TestID))],
					criterion.Message[:min(40, len(criterion.Message))],
					criterion.Value,
					report.StartTime.Format("15:04:05"),
				))
			} else {
				buf.WriteString(fmt.Sprintf("  - [%s] Not evaluated\n",
					report.TestID[:min(12, len(report.TestID))]))
			}
		}
	}
	buf.WriteString("\n")

	// Write to file
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write comparison report: %w", err)
	}

	f.logger.Info("Comparison report generated", "path", outputPath)
	return nil
}

// GetReportPath generates a report file path based on test report and format
func GetReportPath(report *TestReport, format ReportFormat, outputDir string) string {
	timestamp := report.StartTime.Format("20060102-150405")
	ext := string(format)
	filename := fmt.Sprintf("report-%s-%s.%s", timestamp, report.TestID, ext)
	return filepath.Join(outputDir, filename)
}

// Helper function
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// HTML template for report generation
const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Chaos Test Report - {{.TestID}}</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            line-height: 1.6;
            color: #333;
            max-width: 1200px;
            margin: 0 auto;
            padding: 20px;
            background-color: #f5f5f5;
        }
        .container {
            background-color: white;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            padding: 30px;
        }
        h1, h2 {
            color: #2c3e50;
            border-bottom: 2px solid #3498db;
            padding-bottom: 10px;
        }
        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 30px;
            border-radius: 8px 8px 0 0;
            margin: -30px -30px 30px -30px;
        }
        .status {
            display: inline-block;
            padding: 5px 15px;
            border-radius: 4px;
            font-weight: bold;
            margin-left: 10px;
        }
        .status.pass {
            background-color: #27ae60;
            color: white;
        }
        .status.fail {
            background-color: #e74c3c;
            color: white;
        }
        .info-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
            gap: 20px;
            margin: 20px 0;
        }
        .info-box {
            background-color: #ecf0f1;
            padding: 15px;
            border-radius: 4px;
        }
        .info-label {
            font-weight: bold;
            color: #7f8c8d;
            font-size: 0.9em;
            margin-bottom: 5px;
        }
        .info-value {
            font-size: 1.1em;
            color: #2c3e50;
        }
        table {
            width: 100%;
            border-collapse: collapse;
            margin: 20px 0;
        }
        th, td {
            padding: 12px;
            text-align: left;
            border-bottom: 1px solid #ddd;
        }
        th {
            background-color: #3498db;
            color: white;
        }
        tr:hover {
            background-color: #f5f5f5;
        }
        .criterion {
            margin: 15px 0;
            padding: 15px;
            border-left: 4px solid;
            background-color: #f9f9f9;
        }
        .criterion.pass {
            border-left-color: #27ae60;
        }
        .criterion.fail {
            border-left-color: #e74c3c;
        }
        .criterion-name {
            font-weight: bold;
            font-size: 1.1em;
        }
        .criterion-details {
            color: #666;
            margin-top: 5px;
        }
        .audit-entry {
            padding: 10px;
            margin: 5px 0;
            border-radius: 4px;
            background-color: #f9f9f9;
        }
        .audit-success {
            border-left: 4px solid #27ae60;
        }
        .audit-failure {
            border-left: 4px solid #e74c3c;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>Chaos Test Report</h1>
            <p>{{.ScenarioName}}</p>
            <p>Test ID: {{.TestID}}</p>
        </div>

        <h2>Test Summary<span class="status {{statusClass .Success}}">{{if .Success}}PASSED{{else}}FAILED{{end}}</span></h2>
        <div class="info-grid">
            <div class="info-box">
                <div class="info-label">Start Time</div>
                <div class="info-value">{{formatTime .StartTime}}</div>
            </div>
            <div class="info-box">
                <div class="info-label">End Time</div>
                <div class="info-value">{{formatTime .EndTime}}</div>
            </div>
            <div class="info-box">
                <div class="info-label">Duration</div>
                <div class="info-value">{{.Duration}}</div>
            </div>
            <div class="info-box">
                <div class="info-label">Status</div>
                <div class="info-value">{{.Status}}</div>
            </div>
        </div>

        {{if .Targets}}
        <h2>Targets</h2>
        <table>
            <thead>
                <tr>
                    <th>Alias</th>
                    <th>Service Name</th>
                    <th>Container ID</th>
                    <th>IP</th>
                </tr>
            </thead>
            <tbody>
                {{range .Targets}}
                <tr>
                    <td>{{.Alias}}</td>
                    <td>{{.ServiceName}}</td>
                    <td>{{.ContainerID}}</td>
                    <td>{{.IP}}</td>
                </tr>
                {{end}}
            </tbody>
        </table>
        {{end}}

        {{if .Faults}}
        <h2>Faults Injected</h2>
        <table>
            <thead>
                <tr>
                    <th>Phase</th>
                    <th>Type</th>
                    <th>Target</th>
                    <th>Duration</th>
                </tr>
            </thead>
            <tbody>
                {{range .Faults}}
                <tr>
                    <td>{{.Phase}}</td>
                    <td>{{.Type}}</td>
                    <td>{{.Target}}</td>
                    <td>{{.Duration}}</td>
                </tr>
                {{end}}
            </tbody>
        </table>
        {{end}}

        {{if .SuccessCriteria}}
        <h2>Success Criteria</h2>
        {{range .SuccessCriteria}}
        <div class="criterion {{statusClass .Passed}}">
            <div class="criterion-name">{{statusIcon .Passed}} {{.Name}}</div>
            <div class="criterion-details">
                {{if .Description}}<p>{{.Description}}</p>{{end}}
                <p><strong>Type:</strong> {{.Type}}</p>
                {{if .Query}}<p><strong>Query:</strong> <code>{{.Query}}</code></p>{{end}}
                {{if .Threshold}}<p><strong>Threshold:</strong> {{.Threshold}}</p>{{end}}
                <p><strong>Value:</strong> {{.Value}}</p>
                <p><strong>Message:</strong> {{.Message}}</p>
                <p><strong>Evaluated:</strong> {{formatTime .EvalTime}}</p>
            </div>
        </div>
        {{end}}
        {{end}}

        <h2>Cleanup Summary</h2>
        <div class="info-grid">
            <div class="info-box">
                <div class="info-label">Total Actions</div>
                <div class="info-value">{{.CleanupSummary.TotalActions}}</div>
            </div>
            <div class="info-box">
                <div class="info-label">Succeeded</div>
                <div class="info-value">{{.CleanupSummary.Succeeded}}</div>
            </div>
            <div class="info-box">
                <div class="info-label">Failed</div>
                <div class="info-value">{{.CleanupSummary.Failed}}</div>
            </div>
        </div>

        {{if .Errors}}
        <h2>Errors</h2>
        <ul>
            {{range .Errors}}
            <li>{{.}}</li>
            {{end}}
        </ul>
        {{end}}

        <p style="text-align: center; color: #7f8c8d; margin-top: 30px;">
            Generated by Chaos Runner • {{formatTime .EndTime}}
        </p>
    </div>
</body>
</html>
`
