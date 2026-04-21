package reporting

import (
	"time"

	"github.com/jihwankim/chaos-utils/pkg/core/cleanup"
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

	// FaultInstalls is the total number of (container, faultType) installs
	// executed during INJECT. For single-fault scenarios it equals
	// len(Faults); for compound scenarios that target multiple containers
	// (or multiple fault types on the same container post-F-02) it is
	// strictly greater and reflects the true count of kernel-level
	// installs that teardown had to remove.
	FaultInstalls int `json:"fault_installs"`

	// Success criteria evaluation
	SuccessCriteria []CriterionResult `json:"success_criteria,omitempty"`

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
	Alias       string `json:"alias"`
	ServiceName string `json:"service_name"`
	ContainerID string `json:"container_id"`
	IP          string `json:"ip,omitempty"`
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
