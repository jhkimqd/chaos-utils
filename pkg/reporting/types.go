package reporting

import (
	"time"

	"github.com/jihwankim/chaos-utils/pkg/core/cleanup"
	"github.com/jihwankim/chaos-utils/pkg/monitoring/detector"
)

// TestReport represents a complete test execution report
type TestReport struct {
	// Test metadata
	TestID       string    `json:"test_id"`
	ScenarioName string    `json:"scenario_name"`
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	Duration     string    `json:"duration"`

	// Test result
	Status  TestStatus `json:"status"`
	Success bool       `json:"success"`
	Message string     `json:"message,omitempty"`

	// Scenario details
	Targets []TargetInfo `json:"targets"`
	Faults  []FaultInfo  `json:"faults"`

	// Success criteria evaluation
	SuccessCriteria []CriterionResult `json:"success_criteria,omitempty"`

	// Metrics collected during test
	Metrics []MetricTimeSeries `json:"metrics,omitempty"`

	// Cleanup audit
	CleanupSummary cleanup.CleanupSummary `json:"cleanup_summary"`
	CleanupLog     []cleanup.AuditEntry   `json:"cleanup_log,omitempty"`

	// Errors encountered
	Errors []string `json:"errors,omitempty"`
}

// TestStatus represents the status of a test
type TestStatus string

const (
	StatusRunning   TestStatus = "running"
	StatusCompleted TestStatus = "completed"
	StatusFailed    TestStatus = "failed"
	StatusStopped   TestStatus = "stopped"
)

// TargetInfo contains information about a test target
type TargetInfo struct {
	Alias       string            `json:"alias"`
	ServiceName string            `json:"service_name"`
	ContainerID string            `json:"container_id"`
	IP          string            `json:"ip,omitempty"`
	Ports       map[string]uint16 `json:"ports,omitempty"`
}

// FaultInfo contains information about an injected fault
type FaultInfo struct {
	Phase       string                 `json:"phase"`
	Type        string                 `json:"type"`
	Target      string                 `json:"target"`
	Description string                 `json:"description,omitempty"`
	StartTime   time.Time              `json:"start_time"`
	EndTime     time.Time              `json:"end_time,omitempty"`
	Duration    string                 `json:"duration,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// CriterionResult contains success criterion evaluation result
type CriterionResult struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Type        string    `json:"type"`
	Query       string    `json:"query,omitempty"`
	Threshold   string    `json:"threshold,omitempty"`
	Passed      bool      `json:"passed"`
	Value       float64   `json:"value,omitempty"`
	Message     string    `json:"message"`
	Critical    bool      `json:"critical"`
	EvalTime    time.Time `json:"eval_time"`
}

// MetricTimeSeries contains time-series data for a metric
type MetricTimeSeries struct {
	Name    string        `json:"name"`
	Samples []MetricPoint `json:"samples"`
}

// MetricPoint represents a single metric data point
type MetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// ConvertDetectorResult converts detector.CriterionResult to reporting.CriterionResult
func ConvertDetectorResult(dr *detector.CriterionResult) CriterionResult {
	return CriterionResult{
		Name:        dr.Criterion.Name,
		Description: dr.Criterion.Description,
		Type:        dr.Criterion.Type,
		Query:       dr.Criterion.Query,
		Threshold:   dr.Criterion.Threshold,
		Passed:      dr.Passed,
		Value:       dr.LastValue,
		Message:     dr.Message,
		Critical:    dr.Criterion.Critical,
		EvalTime:    dr.LastChecked,
	}
}

// LiveTestState represents the current state of a running test
type LiveTestState struct {
	TestID       string        `json:"test_id"`
	ScenarioName string        `json:"scenario_name"`
	State        string        `json:"state"`
	StartTime    time.Time     `json:"start_time"`
	Elapsed      time.Duration `json:"elapsed"`

	// Active faults
	ActiveFaults []FaultInfo `json:"active_faults,omitempty"`

	// Latest metrics
	LatestMetrics map[string]float64 `json:"latest_metrics,omitempty"`

	// Success criteria status
	CriteriaStatus []CriterionResult `json:"criteria_status,omitempty"`
}
