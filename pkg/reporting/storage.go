package reporting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Storage handles persistence of test reports
type Storage struct {
	outputDir  string
	keepLastN  int
	logger     *Logger
}

// NewStorage creates a new storage instance
func NewStorage(outputDir string, keepLastN int, logger *Logger) (*Storage, error) {
	// Create output directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	return &Storage{
		outputDir: outputDir,
		keepLastN: keepLastN,
		logger:    logger,
	}, nil
}

// SaveReport saves a test report to a JSON file
func (s *Storage) SaveReport(report *TestReport) (string, error) {
	// Generate filename: test-<timestamp>-<testID>.json
	timestamp := report.StartTime.Format("20060102-150405")
	filename := fmt.Sprintf("test-%s-%s.json", timestamp, report.TestID)
	filepath := filepath.Join(s.outputDir, filename)

	// Marshal report to JSON with indentation
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal report: %w", err)
	}

	// Write to file
	if err := os.WriteFile(filepath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write report file: %w", err)
	}

	s.logger.Info("Test report saved", "path", filepath)

	// Cleanup old reports if necessary
	if s.keepLastN > 0 {
		if err := s.cleanupOldReports(); err != nil {
			s.logger.Warn("Failed to cleanup old reports", "error", err)
		}
	}

	return filepath, nil
}

// LoadReport loads a test report from a JSON file
func (s *Storage) LoadReport(filepath string) (*TestReport, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to read report file: %w", err)
	}

	var report TestReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("failed to unmarshal report: %w", err)
	}

	return &report, nil
}

// ListReports lists all test reports in the output directory
func (s *Storage) ListReports() ([]ReportSummary, error) {
	entries, err := os.ReadDir(s.outputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read output directory: %w", err)
	}

	summaries := make([]ReportSummary, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		// Load report
		path := filepath.Join(s.outputDir, entry.Name())
		report, err := s.LoadReport(path)
		if err != nil {
			s.logger.Warn("Failed to load report", "path", path, "error", err)
			continue
		}

		// Create summary
		summaries = append(summaries, ReportSummary{
			TestID:       report.TestID,
			ScenarioName: report.ScenarioName,
			StartTime:    report.StartTime,
			Duration:     report.Duration,
			Status:       report.Status,
			Success:      report.Success,
			Filepath:     path,
		})
	}

	// Sort by start time (newest first)
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].StartTime.After(summaries[j].StartTime)
	})

	return summaries, nil
}

// FindReportByTestID finds a report by test ID
func (s *Storage) FindReportByTestID(testID string) (*TestReport, error) {
	summaries, err := s.ListReports()
	if err != nil {
		return nil, err
	}

	for _, summary := range summaries {
		if summary.TestID == testID {
			return s.LoadReport(summary.Filepath)
		}
	}

	return nil, fmt.Errorf("report not found for test ID: %s", testID)
}

// cleanupOldReports removes old report files, keeping only the last N
func (s *Storage) cleanupOldReports() error {
	summaries, err := s.ListReports()
	if err != nil {
		return err
	}

	if len(summaries) <= s.keepLastN {
		return nil
	}

	// Delete oldest reports
	toDelete := summaries[s.keepLastN:]
	for _, summary := range toDelete {
		if err := os.Remove(summary.Filepath); err != nil {
			s.logger.Warn("Failed to delete old report", "path", summary.Filepath, "error", err)
		} else {
			s.logger.Debug("Deleted old report", "path", summary.Filepath)
		}
	}

	return nil
}

// GetOutputDir returns the output directory path
func (s *Storage) GetOutputDir() string {
	return s.outputDir
}

// ReportSummary contains a summary of a test report
type ReportSummary struct {
	TestID       string     `json:"test_id"`
	ScenarioName string     `json:"scenario_name"`
	StartTime    time.Time  `json:"start_time"`
	Duration     string     `json:"duration"`
	Status       TestStatus `json:"status"`
	Success      bool       `json:"success"`
	Filepath     string     `json:"filepath"`
}
